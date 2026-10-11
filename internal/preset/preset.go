// Package preset holds editing templates: a reusable set of questions for
// Clef-omni, what to do with each answer, and the cut settings.
package preset

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"flashcut/internal/clef"
	"flashcut/internal/editor"
	"flashcut/internal/media"
)

// FormatVersion is written into exported templates.
const FormatVersion = 1

// Question is one template question.
type Question struct {
	// Key identifies the question within the template, e.g. "kill".
	Key string `json:"key"`
	// Label is the short name shown in the app, e.g. "击杀".
	Label string `json:"label"`
	// Type is "noul" (yes/no), "choice" or "score".
	Type string `json:"type"`
	// Instructions is the question for the model. English tends to work
	// best; mention what it looks or sounds like on screen.
	Instructions string `json:"instructions"`
	// Yes and No describe a yes and a no answer (noul only, optional).
	Yes string `json:"yes,omitempty"`
	No  string `json:"no,omitempty"`
	// Options are the choices, id → description (choice only, 2–255).
	Options map[string]string `json:"options,omitempty"`
	// Levels are the ordered levels, lowest first (score only, 2–10).
	Levels []string `json:"levels,omitempty"`
	// Use says what a match does: keep, drop, boost, marker, opening, ending.
	Use editor.Mode `json:"use"`
	// Threshold is the minimum probability for noul and choice, or the
	// minimum level for score.
	Threshold float64 `json:"threshold"`
	// Match lists the choice options that count as a match.
	Match []string `json:"match,omitempty"`
	// Marker names the effect a match triggers ("highlight", "transition"…).
	Marker string `json:"marker,omitempty"`
}

// Template is a reusable editing preset.
type Template struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Category groups templates in the picker, e.g. "电竞".
	Category string `json:"category"`
	Builtin  bool   `json:"builtin"`

	Questions []Question `json:"questions"`

	SegmentSeconds    float64       `json:"segmentSeconds"`
	TargetSeconds     float64       `json:"targetSeconds"`
	PadSeconds        float64       `json:"padSeconds"`
	FillGaps          bool          `json:"fillGaps"`
	InterestThreshold float64       `json:"interestThreshold"`
	Quality           media.Quality `json:"quality,omitempty"`
	// Examples are chat suggestions shown with the template.
	Examples []string `json:"examples,omitempty"`

	UpdatedAt time.Time `json:"updatedAt"`
}

var (
	reKey   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)
	allUses = []editor.Mode{editor.ModeKeep, editor.ModeDrop, editor.ModeBoost, editor.ModeMarker, editor.ModeOpening, editor.ModeEnding}
)

// Validate checks t against what the app and Clef accept.
func (t *Template) Validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if strings.TrimSpace(t.Name) == "" {
		add("模板需要一个名字")
	}
	// Clef takes 64 questions per call; one is the built-in interest question
	// and some room is left for chat requests.
	if len(t.Questions) > 48 {
		add("问题最多 48 个，现在有 %d 个", len(t.Questions))
	}
	if t.SegmentSeconds != 0 && (t.SegmentSeconds < 5 || t.SegmentSeconds > 60) {
		add("分段长度要在 5–60 秒之间")
	}
	seen := map[string]bool{}
	for i, q := range t.Questions {
		where := fmt.Sprintf("第 %d 个问题（%s）", i+1, q.Label)
		if !reKey.MatchString(q.Key) {
			add("%s：标识只能用字母、数字、_ . -，最长 40 个字符", where)
		}
		if seen[q.Key] {
			add("%s：标识 %q 重复了", where, q.Key)
		}
		seen[q.Key] = true
		if strings.TrimSpace(q.Label) == "" {
			add("%s：缺少名称", where)
		}
		if strings.TrimSpace(q.Instructions) == "" {
			add("%s：缺少给模型的问题", where)
		}
		if !slices.Contains(allUses, q.Use) {
			add("%s：用途 %q 不认识", where, q.Use)
		}
		switch q.Type {
		case "noul":
			if q.Threshold < 0 || q.Threshold > 1 {
				add("%s：门槛要在 0–1 之间", where)
			}
		case "choice":
			if len(q.Options) < 2 || len(q.Options) > 255 {
				add("%s：选择题需要 2–255 个选项", where)
			}
			if len(q.Match) == 0 && q.Use != editor.ModeMarker {
				add("%s：选择题要指定哪些选项算命中", where)
			}
			for _, m := range q.Match {
				if _, ok := q.Options[m]; !ok {
					add("%s：命中选项 %q 不在选项里", where, m)
				}
			}
		case "score":
			if len(q.Levels) < 2 || len(q.Levels) > 10 {
				add("%s：打分题需要 2–10 个等级", where)
			}
			if q.Threshold < 0 || q.Threshold > float64(len(q.Levels)-1) {
				add("%s：门槛要在 0–%d 级之间", where, len(q.Levels)-1)
			}
		default:
			add("%s：题型 %q 不认识，只能是 noul、choice、score", where, q.Type)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

// ClefQuestion builds the question sent to the model.
func (q Question) ClefQuestion() clef.Question {
	switch q.Type {
	case "choice":
		return clef.Choice(q.Instructions, q.Options)
	case "score":
		return clef.Score(q.Instructions, q.Levels...)
	}
	return clef.Noul(q.Instructions, q.Yes, q.No)
}

// RuleID is stable for the same question content, so re-applying a template
// reuses earlier answers, while editing a question asks it again.
func (q Question) RuleID() string {
	data, _ := json.Marshal(struct {
		T, I, Y, N string
		O          map[string]string
		L          []string
	}{q.Type, q.Instructions, q.Yes, q.No, q.Options, q.Levels})
	sum := sha1.Sum(data)
	return "t-" + q.Key + "-" + hex.EncodeToString(sum[:4])
}

// Apply replaces p's template rules and settings with t's. Chat rules,
// time ranges and manual choices stay. The change can be undone.
func Apply(p *editor.Project, t *Template) {
	p.Checkpoint()
	rules := slices.DeleteFunc(slices.Clone(p.Edits.Rules), func(r editor.Rule) bool { return r.Source == "template" })
	for _, q := range t.Questions {
		cq := q.ClefQuestion()
		rules = append(rules, editor.Rule{
			ID: q.RuleID(), Text: q.Label, Mode: q.Use, Threshold: q.Threshold, Enabled: true,
			CreatedAt: time.Now(), Source: "template", Question: &cq, Match: q.Match, Marker: q.Marker,
		})
	}
	p.Edits.Rules = rules
	if t.TargetSeconds >= 0 {
		p.Edits.TargetSeconds = t.TargetSeconds
	}
	if t.PadSeconds >= 0 {
		p.Edits.PadSeconds = t.PadSeconds
	}
	if t.InterestThreshold > 0 {
		p.Edits.InterestThreshold = t.InterestThreshold
	}
	p.Edits.FillGaps = t.FillGaps
	p.TemplateID, p.TemplateName = t.ID, t.Name
	// A new segment length only applies before anything was analyzed.
	if t.SegmentSeconds > 0 && t.SegmentSeconds != p.SegmentSeconds && p.Tokens == 0 {
		fresh := editor.New(p.ID, p.Source, t.SegmentSeconds)
		p.SegmentSeconds, p.Segments = fresh.SegmentSeconds, fresh.Segments
		p.Edits.Overrides = map[int]bool{}
	}
}

// FromProject turns a project's rules and settings into a new template.
func FromProject(p *editor.Project, name string) *Template {
	t := &Template{
		Version: FormatVersion, ID: NewID(name), Name: name, Category: "我的模板",
		Description:    "由「" + filepath.Base(p.Source.Path) + "」的剪辑要求生成",
		SegmentSeconds: p.SegmentSeconds, TargetSeconds: p.Edits.TargetSeconds, PadSeconds: p.Edits.PadSeconds,
		FillGaps: p.Edits.FillGaps, InterestThreshold: p.Edits.InterestThreshold, UpdatedAt: time.Now(),
	}
	for i, r := range p.Edits.Rules {
		if !r.Enabled {
			continue
		}
		q := Question{Key: fmt.Sprintf("q%d", i+1), Label: r.Text, Use: r.Mode, Threshold: r.Threshold, Match: r.Match, Marker: r.Marker, Type: "noul"}
		if r.Question != nil {
			q.Type = r.Question.Type
			q.Instructions, _ = r.Question.Instructions.(string)
			switch c := r.Question.Criteria.(type) {
			case map[string]string:
				if q.Type == "choice" {
					q.Options = c
				} else {
					q.Yes, q.No = c["true"], c["false"]
				}
			case map[string]any:
				m := map[string]string{}
				for k, v := range c {
					m[k], _ = v.(string)
				}
				if q.Type == "choice" {
					q.Options = m
				} else {
					q.Yes, q.No = m["true"], m["false"]
				}
			case []string:
				q.Levels = c
			case []any:
				for _, v := range c {
					s, _ := v.(string)
					q.Levels = append(q.Levels, s)
				}
			}
		} else {
			// A chat request: keep the exact wording the model was asked.
			cq := p.QuestionFor(r)
			q.Instructions, _ = cq.Instructions.(string)
			if c, ok := cq.Criteria.(map[string]string); ok {
				q.Yes, q.No = c["true"], c["false"]
			}
		}
		t.Questions = append(t.Questions, q)
	}
	return t
}

// NewID makes a file-safe id.
func NewID(name string) string {
	sum := sha1.Sum(fmt.Appendf(nil, "%s|%d", name, time.Now().UnixNano()))
	return "u-" + hex.EncodeToString(sum[:6])
}

// Store keeps user templates as JSON files in a directory.
type Store struct{ Dir string }

// List returns the built-in templates, then the user's, by name.
func (s Store) List() []*Template {
	out := Builtins()
	var mine []*Template
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if t, err := s.load(filepath.Join(s.Dir, e.Name())); err == nil {
			mine = append(mine, t)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })
	return append(out, mine...)
}

// Get returns the template with id.
func (s Store) Get(id string) (*Template, error) {
	for _, t := range s.List() {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("找不到模板 %s", id)
}

func (s Store) load(path string) (*Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse reads a template from JSON and validates it.
func Parse(data []byte) (*Template, error) {
	var t Template
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("不是有效的模板文件：%w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// Save writes t. Built-in templates cannot be overwritten; saving one stores
// a copy under a new id.
func (s Store) Save(t *Template) (*Template, error) {
	c := *t
	if c.Builtin || c.ID == "" || !strings.HasPrefix(c.ID, "u-") {
		c.ID = NewID(c.Name)
		c.Builtin = false
	}
	c.Version = FormatVersion
	c.UpdatedAt = time.Now()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	tmp := filepath.Join(s.Dir, c.ID+".json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, err
	}
	return &c, os.Rename(tmp, filepath.Join(s.Dir, c.ID+".json"))
}

// Delete removes a user template.
func (s Store) Delete(id string) error {
	if !strings.HasPrefix(id, "u-") || strings.ContainsAny(id, `/\.`) {
		return errors.New("内置模板不能删除")
	}
	return os.Remove(filepath.Join(s.Dir, id+".json"))
}

// Export encodes t for sharing.
func Export(t *Template) ([]byte, error) {
	c := *t
	c.Builtin = false
	c.Version = FormatVersion
	return json.MarshalIndent(c, "", "  ")
}
