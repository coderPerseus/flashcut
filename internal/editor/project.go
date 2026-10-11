// Package editor holds a FlashCut project: the source video split into
// segments, the model's answers per segment, the user's editing rules, and
// the selection those rules produce.
package editor

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"flashcut/internal/media"
)

// FormatVersion changes when the saved project format does.
const FormatVersion = 1

// InterestID is the question every segment is asked: how worth watching it
// is, 0 (skip) to 4 (must see).
const InterestID = "interest"

// Mode says whether a rule keeps or removes what it matches.
type Mode string

const (
	ModeKeep Mode = "keep"
	ModeDrop Mode = "drop"
)

// Rule is a content request in the user's words, asked of every segment.
type Rule struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Mode      Mode      `json:"mode"`
	Threshold float64   `json:"threshold"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
}

// RangeRule keeps or removes an exact span of the source.
type RangeRule struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Keep  bool    `json:"keep"`
}

// Segment is a fixed-length slice of the source.
type Segment struct {
	Index int     `json:"index"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	// Answers maps a question id (InterestID or a rule id) to the model's
	// answer: a probability for rules, a level 0–4 for interest.
	Answers map[string]float64 `json:"answers"`
	Error   string             `json:"error,omitempty"`
	// Kept and Reason are set by Select.
	Kept   bool   `json:"kept"`
	Reason string `json:"reason"`
}

// Edits is everything the user can change, kept together for undo.
type Edits struct {
	Rules  []Rule      `json:"rules"`
	Ranges []RangeRule `json:"ranges"`
	// TargetSeconds, when set, caps the length: the best matching segments
	// are kept up to it. Without keep rules, the most interesting segments
	// fill it.
	TargetSeconds float64 `json:"targetSeconds"`
	// InterestThreshold keeps segments at or above this level when no keep
	// rule exists.
	InterestThreshold float64 `json:"interestThreshold"`
	// Overrides are segments the user kept (true) or dropped (false) by hand.
	Overrides map[int]bool `json:"overrides"`
	FillGaps  bool         `json:"fillGaps"`
	// PadSeconds extends each kept clip on both sides so moments near a
	// segment boundary keep their lead-in.
	PadSeconds float64 `json:"padSeconds"`
}

// Message is one chat message.
type Message struct {
	Role string    `json:"role"` // "user" or "assistant"
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Project is one source video being edited.
type Project struct {
	ID             string          `json:"id"`
	Version        int             `json:"version"`
	Source         media.VideoInfo `json:"source"`
	SegmentSeconds float64         `json:"segmentSeconds"`
	Edits          Edits           `json:"edits"`
	Segments       []Segment       `json:"segments"`
	Chat           []Message       `json:"chat"`
	History        []Edits         `json:"history"`
	Tokens         int             `json:"tokens"`
	// NextRuleID numbers rules; ids are never reused, so answers cached for
	// a deleted rule cannot be mistaken for a new one.
	NextRuleID int       `json:"nextRuleId"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// DefaultSegmentSeconds balances cut precision against per-call overhead.
const DefaultSegmentSeconds = 15

// ProjectID identifies a source file by path, size and modification time.
func ProjectID(info media.VideoInfo, modTime time.Time) string {
	sum := sha1.Sum(fmt.Appendf(nil, "%s|%d|%d", info.Path, info.Size, modTime.Unix()))
	return hex.EncodeToString(sum[:8])
}

// New splits info into segments of segLen seconds.
func New(id string, info media.VideoInfo, segLen float64) *Project {
	if segLen < 5 || segLen > 60 {
		segLen = DefaultSegmentSeconds
	}
	p := &Project{
		ID: id, Version: FormatVersion, Source: info, SegmentSeconds: segLen,
		Edits:     Edits{InterestThreshold: 2.5, FillGaps: true, PadSeconds: 2, Overrides: map[int]bool{}},
		UpdatedAt: time.Now(),
	}
	n := int(math.Ceil(info.Duration / segLen))
	for i := range n {
		p.Segments = append(p.Segments, Segment{
			Index: i, Start: float64(i) * segLen, End: math.Min(float64(i+1)*segLen, info.Duration),
			Answers: map[string]float64{},
		})
	}
	// Merge a trailing sliver too short to judge into the previous segment.
	if n > 1 && p.Segments[n-1].End-p.Segments[n-1].Start < 3 {
		p.Segments[n-2].End = info.Duration
		p.Segments = p.Segments[:n-1]
	}
	return p
}

// Questions lists the question ids every segment must answer.
func (p *Project) Questions() []string {
	ids := []string{InterestID}
	for _, r := range p.Edits.Rules {
		ids = append(ids, r.ID)
	}
	return ids
}

// Pending returns, per segment index, the questions it still has to answer.
func (p *Project) Pending() map[int][]string {
	out := map[int][]string{}
	for _, s := range p.Segments {
		for _, q := range p.Questions() {
			if _, ok := s.Answers[q]; !ok {
				out[s.Index] = append(out[s.Index], q)
			}
		}
	}
	return out
}

// Rule returns the rule with id.
func (p *Project) Rule(id string) *Rule {
	for i := range p.Edits.Rules {
		if p.Edits.Rules[i].ID == id {
			return &p.Edits.Rules[i]
		}
	}
	return nil
}

// Checkpoint saves the current edits for Undo.
func (p *Project) Checkpoint() {
	p.History = append(p.History, p.Edits.clone())
	if len(p.History) > 50 {
		p.History = p.History[len(p.History)-50:]
	}
}

// Undo restores the edits before the last change.
func (p *Project) Undo() bool {
	if len(p.History) == 0 {
		return false
	}
	p.Edits = p.History[len(p.History)-1]
	p.History = p.History[:len(p.History)-1]
	return true
}

func (e Edits) clone() Edits {
	c := e
	c.Rules = slices.Clone(e.Rules)
	c.Ranges = slices.Clone(e.Ranges)
	c.Overrides = make(map[int]bool, len(e.Overrides))
	for k, v := range e.Overrides {
		c.Overrides[k] = v
	}
	return c
}

// DefaultThreshold favors recall for keep rules (missing a requested moment
// is worse than an extra clip) and precision for drop rules (cutting
// something the user wanted is worse than leaving a stray clip).
func DefaultThreshold(m Mode) float64 {
	if m == ModeDrop {
		return 0.5
	}
	return 0.35
}

// AddRule adds a content rule and returns it.
func (p *Project) AddRule(text string, mode Mode) Rule {
	p.NextRuleID++
	r := Rule{
		ID:   fmt.Sprintf("rule%d", p.NextRuleID),
		Text: strings.TrimSpace(text), Mode: mode, Threshold: DefaultThreshold(mode), Enabled: true, CreatedAt: time.Now(),
	}
	p.Edits.Rules = append(p.Edits.Rules, r)
	return r
}

// Select decides which segments to keep.
func (p *Project) Select() {
	e := p.Edits
	var keepRules, dropRules []Rule
	for _, r := range e.Rules {
		if !r.Enabled {
			continue
		}
		if r.Mode == ModeDrop {
			dropRules = append(dropRules, r)
		} else {
			keepRules = append(keepRules, r)
		}
	}
	type cand struct {
		i        int
		priority float64
	}
	var cands []cand
	for i := range p.Segments {
		s := &p.Segments[i]
		s.Kept, s.Reason = false, ""
		interest, ok := s.Answers[InterestID]
		if !ok {
			s.Reason = "待分析"
			continue
		}
		if r, prob, hit := best(s, dropRules); hit {
			s.Reason = fmt.Sprintf("去掉：%s（%d%%）", r.Text, pct(prob))
			continue
		}
		var priority float64
		if len(keepRules) > 0 {
			r, prob, hit := best(s, keepRules)
			priority = prob
			if hit {
				s.Kept, s.Reason = true, fmt.Sprintf("保留：%s（%d%%）", r.Text, pct(prob))
			} else {
				s.Reason = "不符合保留要求"
			}
		} else {
			priority = interest / 4
			if interest >= e.InterestThreshold {
				s.Kept, s.Reason = true, fmt.Sprintf("精彩分 %.1f", interest)
			} else {
				s.Reason = fmt.Sprintf("精彩分 %.1f，偏低", interest)
			}
		}
		// Ties are broken by interest so the budget prefers lively segments.
		cands = append(cands, cand{i, priority + interest/1000})
	}

	if e.TargetSeconds > 0 {
		// With keep rules the target is a cap on what they matched; without,
		// the best segments fill it.
		if len(keepRules) > 0 {
			cands = slices.DeleteFunc(cands, func(c cand) bool { return !p.Segments[c.i].Kept })
		}
		sort.SliceStable(cands, func(a, b int) bool { return cands[a].priority > cands[b].priority })
		for _, c := range cands {
			p.Segments[c.i].Kept = false
		}
		total := 0.0
		for _, c := range cands {
			s := &p.Segments[c.i]
			if total >= e.TargetSeconds {
				break
			}
			s.Kept = true
			if !strings.HasPrefix(s.Reason, "保留") && !strings.HasPrefix(s.Reason, "精彩") {
				s.Reason = "按时长补入"
			}
			total += s.End - s.Start
		}
		for _, c := range cands {
			s := &p.Segments[c.i]
			if !s.Kept && (strings.HasPrefix(s.Reason, "保留") || strings.HasPrefix(s.Reason, "精彩")) {
				s.Reason += "，超出目标时长"
			}
		}
	}

	if e.FillGaps {
		for i := 1; i+1 < len(p.Segments); i++ {
			s := &p.Segments[i]
			if !s.Kept && p.Segments[i-1].Kept && p.Segments[i+1].Kept && !strings.HasPrefix(s.Reason, "去掉") && s.Reason != "待分析" {
				s.Kept, s.Reason = true, "补齐前后片段之间的空隙"
			}
		}
	}
	for i, keep := range e.Overrides {
		if i >= 0 && i < len(p.Segments) {
			p.Segments[i].Kept = keep
			if keep {
				p.Segments[i].Reason = "手动保留"
			} else {
				p.Segments[i].Reason = "手动去掉"
			}
		}
	}

	// Padding, gap filling and range rules can push the result past the
	// target; trim the least relevant segments until it fits.
	if e.TargetSeconds > 0 {
		order := make([]cand, len(cands))
		copy(order, cands)
		sort.SliceStable(order, func(a, b int) bool { return order[a].priority < order[b].priority })
		prio := map[int]float64{}
		for _, c := range order {
			prio[c.i] = c.priority
		}
		for p.keptSeconds() > e.TargetSeconds+0.5 {
			// Drop the kept, unpinned segment with the lowest priority;
			// gap fillers (no priority) go first.
			victim, low := -1, math.Inf(1)
			for i, s := range p.Segments {
				if _, pinned := e.Overrides[i]; !s.Kept || pinned {
					continue
				}
				pr, ok := prio[i]
				if !ok {
					pr = -1
				}
				if pr < low {
					victim, low = i, pr
				}
			}
			if victim < 0 {
				break
			}
			p.Segments[victim].Kept = false
			p.Segments[victim].Reason += "，超出目标时长"
		}
	}
}

func (p *Project) keptSeconds() float64 {
	total := 0.0
	for _, r := range p.Ranges() {
		total += r.End - r.Start
	}
	return total
}

func best(s *Segment, rules []Rule) (Rule, float64, bool) {
	var top Rule
	topP, hit := -1.0, false
	for _, r := range rules {
		prob, ok := s.Answers[r.ID]
		if !ok {
			continue
		}
		if prob >= r.Threshold && (!hit || prob > topP) {
			top, topP, hit = r, prob, true
		} else if !hit && prob > topP {
			top, topP = r, prob
		}
	}
	return top, math.Max(topP, 0), hit
}

func pct(p float64) int { return int(math.Round(p * 100)) }

// Ranges returns the spans to export: kept segments merged, plus range
// rules applied at their exact times.
func (p *Project) Ranges() []media.Range {
	var spans []media.Range
	pad := math.Max(0, math.Min(p.Edits.PadSeconds, 10))
	for _, s := range p.Segments {
		if s.Kept {
			spans = append(spans, media.Range{Start: math.Max(0, s.Start-pad), End: math.Min(p.Source.Duration, s.End+pad)})
		}
	}
	for _, r := range p.Edits.Ranges {
		start, end := math.Max(0, r.Start), math.Min(p.Source.Duration, r.End)
		if end <= start {
			continue
		}
		if r.Keep {
			spans = append(spans, media.Range{Start: start, End: end})
		} else {
			spans = subtract(spans, media.Range{Start: start, End: end})
		}
	}
	spans = merge(spans)
	out := spans[:0]
	for _, s := range spans {
		if s.End-s.Start >= 1 {
			out = append(out, s)
		}
	}
	return out
}

func merge(spans []media.Range) []media.Range {
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	var out []media.Range
	for _, s := range spans {
		if n := len(out); n > 0 && s.Start <= out[n-1].End+0.01 {
			out[n-1].End = math.Max(out[n-1].End, s.End)
		} else {
			out = append(out, s)
		}
	}
	return out
}

func subtract(spans []media.Range, cut media.Range) []media.Range {
	var out []media.Range
	for _, s := range spans {
		if cut.End <= s.Start || cut.Start >= s.End {
			out = append(out, s)
			continue
		}
		if cut.Start > s.Start {
			out = append(out, media.Range{Start: s.Start, End: cut.Start})
		}
		if cut.End < s.End {
			out = append(out, media.Range{Start: cut.End, End: s.End})
		}
	}
	return out
}

// Summary describes the current selection.
type Summary struct {
	Segments      int     `json:"segments"`
	Analyzed      int     `json:"analyzed"`
	Failed        int     `json:"failed"`
	KeptSegments  int     `json:"keptSegments"`
	KeptSeconds   float64 `json:"keptSeconds"`
	Clips         int     `json:"clips"`
	Tokens        int     `json:"tokens"`
	CostUSD       float64 `json:"costUsd"`
	PendingTokens int     `json:"pendingTokens"`
}

// TokensPerSecond estimates Clef-omni input tokens per second of video.
const TokensPerSecond = 260

// Summarize reports the selection and the cost so far.
func (p *Project) Summarize() Summary {
	sum := Summary{Segments: len(p.Segments), Tokens: p.Tokens}
	pending := p.Pending()
	for _, s := range p.Segments {
		if _, ok := pending[s.Index]; !ok {
			sum.Analyzed++
		} else {
			sum.PendingTokens += int((s.End - s.Start) * TokensPerSecond)
		}
		if s.Error != "" {
			sum.Failed++
		}
		if s.Kept {
			sum.KeptSegments++
		}
	}
	for _, r := range p.Ranges() {
		sum.Clips++
		sum.KeptSeconds += r.End - r.Start
	}
	sum.CostUSD = float64(p.Tokens) / 1e6 * 0.15
	return sum
}

// Save writes the project to dir/<id>/project.json atomically.
func (p *Project) Save(dir string) error {
	p.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(p, "", " ")
	if err != nil {
		return err
	}
	pdir := filepath.Join(dir, p.ID)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(pdir, "project.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(pdir, "project.json"))
}

// Load reads a saved project.
func Load(dir, id string) (*Project, error) {
	data, err := os.ReadFile(filepath.Join(dir, id, "project.json"))
	if err != nil {
		return nil, err
	}
	var p Project
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if p.Version != FormatVersion {
		return nil, errors.New("项目格式已过期")
	}
	if p.Edits.Overrides == nil {
		p.Edits.Overrides = map[int]bool{}
	}
	for i := range p.Segments {
		if p.Segments[i].Answers == nil {
			p.Segments[i].Answers = map[string]float64{}
		}
	}
	return &p, nil
}

// Recent lists saved projects, newest first.
func Recent(dir string, limit int) []*Project {
	entries, _ := os.ReadDir(dir)
	var out []*Project
	for _, e := range entries {
		if p, err := Load(dir, e.Name()); err == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
