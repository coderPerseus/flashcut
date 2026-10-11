package highlight

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Segment is one fixed-length slice of the source and the model's verdict.
type Segment struct {
	Index   int      `json:"index"`
	Start   float64  `json:"start"`
	End     float64  `json:"end"`
	Verdict *Verdict `json:"verdict"`
	Error   string   `json:"error,omitempty"`
	// Kept is decided by Select; users may override it.
	Kept bool `json:"kept"`
}

// Analysis is every segment of one source video, cached on disk so a
// re-render with other thresholds does not call the model again.
type Analysis struct {
	Source         VideoInfo `json:"source"`
	SegmentSeconds float64   `json:"segmentSeconds"`
	Version        int       `json:"version"`
	Judge          string    `json:"judge"`
	Segments       []Segment `json:"segments"`
}

// Options tune segmentation and selection.
type Options struct {
	SegmentSeconds float64 `json:"segmentSeconds"`
	Concurrency    int     `json:"concurrency"`
	// A segment is kept when its keep probability reaches KeepThreshold,
	// its excitement score reaches ScoreThreshold, or a champion is killed
	// in it with probability KillThreshold.
	KeepThreshold  float64 `json:"keepThreshold"`
	ScoreThreshold float64 `json:"scoreThreshold"`
	KillThreshold  float64 `json:"killThreshold"`
	// KeepOpening and KeepEnding keep the start and the end of the game.
	KeepOpening bool `json:"keepOpening"`
	KeepEnding  bool `json:"keepEnding"`
	// FillGaps keeps a single dropped segment between two kept ones, so a
	// fight is not cut in the middle.
	FillGaps bool `json:"fillGaps"`
}

// DefaultOptions keep about 15% of an LCK broadcast (48 min → 7 min).
func DefaultOptions() Options {
	return Options{SegmentSeconds: 20, Concurrency: 4, KeepThreshold: 0.9, ScoreThreshold: 2.5, KillThreshold: 0.4, KeepOpening: true, KeepEnding: true, FillGaps: true}
}

// WorkDir is where analysis and parts for src are kept, under base.
func WorkDir(base, src string) string {
	sum := sha1.Sum([]byte(src))
	name := filepath.Base(src)
	name = name[:len(name)-len(filepath.Ext(name))]
	return filepath.Join(base, name+"-"+hex.EncodeToString(sum[:4]))
}

// Progress reports analysis progress.
type Progress struct {
	Done    int      `json:"done"`
	Total   int      `json:"total"`
	Segment *Segment `json:"segment,omitempty"`
}

// Analyze splits src into segments and asks the judge about each. Results
// are cached in workDir/analysis.json; segments already judged are reused.
func Analyze(ctx context.Context, judge Judge, src, workDir string, opts Options, onProgress func(Progress)) (*Analysis, error) {
	info, err := Probe(ctx, src)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	segLen := opts.SegmentSeconds
	if segLen < 5 || segLen > 60 {
		segLen = 20
	}
	a := &Analysis{Source: info, SegmentSeconds: segLen, Version: QuestionsVersion, Judge: judge.Name()}
	n := int(math.Ceil(info.Duration / segLen))
	for i := range n {
		end := math.Min(float64(i+1)*segLen, info.Duration)
		a.Segments = append(a.Segments, Segment{Index: i, Start: float64(i) * segLen, End: end})
	}
	// Drop a trailing sliver too short to judge; merge it into the previous segment.
	if n > 1 && a.Segments[n-1].End-a.Segments[n-1].Start < 3 {
		a.Segments[n-2].End = info.Duration
		a.Segments = a.Segments[:n-1]
	}
	if cached, err := LoadAnalysis(workDir); err == nil && cached.Version == QuestionsVersion && cached.SegmentSeconds == segLen && len(cached.Segments) == len(a.Segments) {
		for i, s := range cached.Segments {
			if s.Verdict != nil {
				a.Segments[i].Verdict = s.Verdict
			}
		}
	}

	var (
		mu   sync.Mutex
		done int
		wg   sync.WaitGroup
	)
	todo := make(chan int)
	report := func(i int) {
		mu.Lock()
		done++
		p := Progress{Done: done, Total: len(a.Segments)}
		if i >= 0 {
			seg := a.Segments[i]
			p.Segment = &seg
		}
		mu.Unlock()
		if onProgress != nil {
			onProgress(p)
		}
	}
	workers := max(1, min(opts.Concurrency, 8))
	for range workers {
		wg.Go(func() {
			for i := range todo {
				seg := a.Segments[i]
				v, err := judgeSegment(ctx, judge, src, workDir, seg)
				mu.Lock()
				if err != nil {
					a.Segments[i].Error = err.Error()
				} else {
					a.Segments[i].Verdict = &v
					a.Segments[i].Error = ""
				}
				mu.Unlock()
				report(i)
			}
		})
	}
	for i, s := range a.Segments {
		if s.Verdict != nil {
			report(i)
			continue
		}
		select {
		case todo <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(todo)
	wg.Wait()
	if err := a.Save(workDir); err != nil {
		return a, err
	}
	if ctx.Err() != nil {
		return a, ctx.Err()
	}
	failed := 0
	for _, s := range a.Segments {
		if s.Verdict == nil {
			failed++
		}
	}
	if failed == len(a.Segments) {
		return a, fmt.Errorf("every segment failed; first error: %s", a.Segments[0].Error)
	}
	return a, nil
}

func judgeSegment(ctx context.Context, judge Judge, src, workDir string, seg Segment) (Verdict, error) {
	clip := filepath.Join(workDir, fmt.Sprintf("seg-%04d.mp4", seg.Index))
	defer os.Remove(clip)
	if err := ExtractPreview(ctx, src, clip, seg.Start, seg.End-seg.Start); err != nil {
		return Verdict{}, err
	}
	data, err := os.ReadFile(clip)
	if err != nil {
		return Verdict{}, err
	}
	if len(data) > 15<<20 {
		return Verdict{}, fmt.Errorf("preview clip is %d MiB, over the 16 MiB limit", len(data)>>20)
	}
	return judge.Judge(ctx, data, seg.End-seg.Start)
}

// Select marks which segments to keep and returns the merged time ranges.
func (a *Analysis) Select(opts Options) []Range {
	for i := range a.Segments {
		s := &a.Segments[i]
		v := s.Verdict
		s.Kept = v != nil && (v.Keep >= opts.KeepThreshold || v.Score >= opts.ScoreThreshold || v.Kill >= opts.KillThreshold)
	}
	if opts.KeepOpening {
		a.keepFirst(func(v *Verdict) bool { return v.Opening >= 0.5 }, false)
	}
	if opts.KeepEnding {
		a.keepFirst(func(v *Verdict) bool { return v.Ending >= 0.5 }, true)
	}
	if opts.FillGaps {
		for i := 1; i+1 < len(a.Segments); i++ {
			if !a.Segments[i].Kept && a.Segments[i-1].Kept && a.Segments[i+1].Kept {
				a.Segments[i].Kept = true
			}
		}
	}
	return a.Ranges()
}

// keepFirst keeps the run of consecutive segments matching match that comes
// first in the video (or last, when fromEnd), so a stray false positive in
// the middle of the game is not kept as the opening or the ending.
func (a *Analysis) keepFirst(match func(*Verdict) bool, fromEnd bool) {
	n := len(a.Segments)
	at := func(i int) *Segment {
		if fromEnd {
			return &a.Segments[n-1-i]
		}
		return &a.Segments[i]
	}
	found := false
	for i := range n {
		s := at(i)
		if s.Verdict != nil && match(s.Verdict) {
			s.Kept, found = true, true
		} else if found {
			return
		}
	}
}

// Ranges merges consecutive kept segments into time ranges.
func (a *Analysis) Ranges() []Range {
	var out []Range
	for _, s := range a.Segments {
		if !s.Kept {
			continue
		}
		if n := len(out); n > 0 && math.Abs(out[n-1].End-s.Start) < 0.01 {
			out[n-1].End = s.End
		} else {
			out = append(out, Range{Start: s.Start, End: s.End})
		}
	}
	return out
}

// Stats summarizes an analysis.
type Stats struct {
	Segments     int            `json:"segments"`
	Kept         int            `json:"kept"`
	Failed       int            `json:"failed"`
	KeptSeconds  float64        `json:"keptSeconds"`
	TotalSeconds float64        `json:"totalSeconds"`
	Tokens       int            `json:"tokens"`
	Scenes       map[string]int `json:"scenes"`
}

func (a *Analysis) Stats() Stats {
	st := Stats{Segments: len(a.Segments), TotalSeconds: a.Source.Duration, Scenes: map[string]int{}}
	for _, s := range a.Segments {
		if s.Verdict == nil {
			st.Failed++
			continue
		}
		st.Tokens += s.Verdict.Tokens
		st.Scenes[s.Verdict.Scene]++
		if s.Kept {
			st.Kept++
			st.KeptSeconds += s.End - s.Start
		}
	}
	return st
}

// TopScenes lists scene labels by count, for logs.
func (st Stats) TopScenes() []string {
	var keys []string
	for k := range st.Scenes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return st.Scenes[keys[i]] > st.Scenes[keys[j]] })
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = fmt.Sprintf("%s=%d", k, st.Scenes[k])
	}
	return out
}

func (a *Analysis) Save(workDir string) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(workDir, "analysis.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(workDir, "analysis.json"))
}

func LoadAnalysis(workDir string) (*Analysis, error) {
	data, err := os.ReadFile(filepath.Join(workDir, "analysis.json"))
	if err != nil {
		return nil, err
	}
	var a Analysis
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}
