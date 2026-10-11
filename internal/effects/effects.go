// Package effects adds Remotion motion graphics to an exported cut: an
// intro title, transitions at each cut, "高能" moments, the ending and an
// end card. The moments come from the project's template markers.
package effects

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"flashcut/internal/editor"
	"flashcut/internal/media"
)

//go:embed remotion/package.json remotion/render.mjs remotion/src
var project embed.FS

// Event is one effect on the output timeline, in seconds.
type Event struct {
	Kind  string  `json:"kind"` // transition, highlight, opening, ending
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Label string  `json:"label"`
}

// Plan is the input of the Remotion composition.
type Plan struct {
	Video        string  `json:"video"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	FPS          int     `json:"fps"`
	Duration     float64 `json:"duration"`
	OutroSeconds float64 `json:"outroSeconds"`
	Title        string  `json:"title"`
	Subtitle     string  `json:"subtitle"`
	Accent       string  `json:"accent"`
	Style        string  `json:"style"`
	Intro        bool    `json:"intro"`
	ProgressBar  bool    `json:"progressBar"`
	Events       []Event `json:"events"`
	Stats        string  `json:"stats"`
}

// BuildPlan maps the project's marked moments onto the cut's timeline.
// cut describes the exported cut video.
func BuildPlan(p *editor.Project, s editor.EffectSettings, cut media.VideoInfo) Plan {
	ranges := p.Ranges()
	plan := Plan{
		Video: "cut.mp4", Width: even(cut.Width), Height: even(cut.Height), FPS: 30, Duration: cut.Duration,
		Title: s.Title, Subtitle: s.Subtitle, Accent: s.Accent, Style: s.Style,
		Intro: s.Intro, ProgressBar: s.ProgressBar,
	}
	if plan.Title == "" {
		plan.Title = strings.TrimSuffix(filepath.Base(p.Source.Path), filepath.Ext(p.Source.Path))
	}
	if plan.Accent == "" {
		plan.Accent = "#7c5cff"
	}
	if plan.Style != "clean" {
		plan.Style = "esports"
	}
	if s.Outro {
		plan.OutroSeconds = 2.5
		plan.Stats = fmt.Sprintf("全片 %s → 精华 %s", media.Clock(p.Source.Duration), media.Clock(cut.Duration))
	}

	// toOut maps a source span to output spans.
	offsets := make([]float64, len(ranges))
	acc := 0.0
	for i, r := range ranges {
		offsets[i] = acc
		acc += r.End - r.Start
	}
	toOut := func(start, end float64) []media.Range {
		var out []media.Range
		for i, r := range ranges {
			a, b := math.Max(start, r.Start), math.Min(end, r.End)
			if b > a {
				out = append(out, media.Range{Start: offsets[i] + a - r.Start, End: offsets[i] + b - r.Start})
			}
		}
		return out
	}

	type marked struct {
		kind, label string
		span        media.Range // output time
		srcEnd      float64     // source time, to avoid merging across cuts
		srcStart    float64
	}
	var marks []marked
	hasHighlightRule := false
	for _, r := range p.Edits.Rules {
		if !r.Enabled {
			continue
		}
		kind := r.Marker
		switch r.Mode {
		case editor.ModeOpening:
			kind = "opening"
		case editor.ModeEnding:
			kind = "ending"
		}
		if kind == "" {
			continue
		}
		if kind == "highlight" {
			hasHighlightRule = true
		}
		for _, seg := range p.Segments {
			if v, _, ok := seg.Value(r); ok && v >= r.Threshold {
				for _, o := range toOut(seg.Start, seg.End) {
					marks = append(marks, marked{kind, r.Text, o, seg.End, seg.Start})
				}
			}
		}
	}
	// Without highlight markers, the most interesting moments stand in.
	if !hasHighlightRule {
		for _, seg := range p.Segments {
			if v, ok := seg.Answers[editor.InterestID]; ok && v >= 3.3 {
				for _, o := range toOut(seg.Start, seg.End) {
					marks = append(marks, marked{"highlight", "", o, seg.End, seg.Start})
				}
			}
		}
	}
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].span.Start < marks[j].span.Start })

	// Merge touching spans of the same kind; highlights keep the first label.
	var merged []marked
	for _, m := range marks {
		if n := len(merged); n > 0 && merged[n-1].kind == m.kind && m.span.Start <= merged[n-1].span.End+0.5 &&
			m.srcStart <= merged[n-1].srcEnd+0.5 {
			merged[n-1].span.End = math.Max(merged[n-1].span.End, m.span.End)
			merged[n-1].srcEnd = math.Max(merged[n-1].srcEnd, m.srcEnd)
			continue
		}
		merged = append(merged, m)
	}

	var events []Event
	lastHighlight := -100.0
	var ending *Event
	for _, m := range merged {
		switch m.kind {
		case "highlight":
			if !s.Highlights || m.span.Start-lastHighlight < 4 {
				continue
			}
			lastHighlight = m.span.Start
			events = append(events, Event{Kind: "highlight", Start: m.span.Start, End: m.span.End, Label: m.label})
		case "transition":
			if s.Transitions {
				events = append(events, Event{Kind: "transition", Start: m.span.Start, End: m.span.Start})
			}
		case "ending":
			e := Event{Kind: "ending", Start: m.span.Start, End: m.span.End, Label: m.label}
			ending = &e
		}
	}
	if s.Transitions {
		for i := 1; i < len(ranges); i++ {
			events = append(events, Event{Kind: "transition", Start: offsets[i], End: offsets[i]})
		}
	}
	if s.Ending {
		if ending == nil && cut.Duration > 6 {
			ending = &Event{Kind: "ending", Start: cut.Duration - 3, End: cut.Duration}
		}
		if ending != nil {
			ending.End = math.Min(ending.End, cut.Duration)
			ending.Label = "终局时刻"
			events = append(events, *ending)
		}
	}
	// Transitions too close to each other or to the start look like a glitch.
	sort.SliceStable(events, func(i, j int) bool { return events[i].Start < events[j].Start })
	var out []Event
	lastCut := -100.0
	for _, e := range events {
		if e.Kind == "transition" {
			if e.Start < 1 || e.Start-lastCut < 1.5 || e.Start > cut.Duration-0.3 {
				continue
			}
			lastCut = e.Start
		}
		out = append(out, e)
	}
	plan.Events = out
	return plan
}

func even(n int) int {
	if n <= 0 {
		return 1280
	}
	return n &^ 1
}

// Engine installs and runs the Remotion project.
type Engine struct {
	// Dir is where the project and its node_modules live.
	Dir string
}

// Ready reports whether the engine is installed.
func (e Engine) Ready() bool {
	_, err := os.Stat(filepath.Join(e.Dir, "node_modules", "@remotion", "renderer"))
	src, pkg := e.installed()
	return err == nil && src == e.sourceHash() && pkg == e.packageHash()
}

// Install writes the project files and installs its dependencies. The first
// run downloads Remotion (and, on the first render, a headless Chrome).
func (e Engine) Install(ctx context.Context, log func(string)) error {
	if err := fs.WalkDir(project, "remotion", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := project.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(e.Dir, strings.TrimPrefix(path, "remotion/"))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}); err != nil {
		return err
	}
	if _, pkg := e.installed(); pkg != e.packageHash() || !exists(filepath.Join(e.Dir, "node_modules", "@remotion", "renderer")) {
		bin, args, err := installer()
		if err != nil {
			return err
		}
		if log != nil {
			log("正在安装动效引擎（首次需要下载约 100 MB）…")
		}
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir, cmd.Env = e.Dir, toolEnv(bin)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("安装动效引擎失败：%w\n%s", err, tail(out))
		}
	}
	return os.WriteFile(filepath.Join(e.Dir, ".installed"), []byte(e.sourceHash()+"\n"+e.packageHash()), 0o644)
}

// installed returns the source and package.json hashes of the last install.
func (e Engine) installed() (src, pkg string) {
	data, _ := os.ReadFile(filepath.Join(e.Dir, ".installed"))
	src, pkg, _ = strings.Cut(strings.TrimSpace(string(data)), "\n")
	return src, pkg
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (e Engine) packageHash() string {
	data, _ := project.ReadFile("remotion/package.json")
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

func (e Engine) sourceHash() string {
	h := sha1.New()
	_ = fs.WalkDir(project, "remotion", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := project.ReadFile(path)
			h.Write([]byte(path))
			h.Write(data)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

// Render draws plan over cut and writes out. progress gets a stage name
// ("browser", "bundle", "render") and 0–1.
func (e Engine) Render(ctx context.Context, cut string, plan Plan, out string, progress func(stage string, p float64)) error {
	if !e.Ready() {
		if err := e.Install(ctx, func(msg string) {
			if progress != nil {
				progress("install", 0)
			}
		}); err != nil {
			return err
		}
	}
	node, err := lookNode()
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(filepath.Dir(out), ".effects-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	public := filepath.Join(work, "public")
	if err := os.MkdirAll(public, 0o755); err != nil {
		return err
	}
	// A hard link avoids copying the cut; fall back to a copy across volumes.
	if err := os.Link(cut, filepath.Join(public, "cut.mp4")); err != nil {
		if err := copyFile(cut, filepath.Join(public, "cut.mp4")); err != nil {
			return err
		}
	}
	planPath := filepath.Join(work, "plan.json")
	data, _ := json.Marshal(plan)
	if err := os.WriteFile(planPath, data, 0o644); err != nil {
		return err
	}
	tmpOut := filepath.Join(work, "out.mp4")
	cmd := exec.CommandContext(ctx, node, "render.mjs", planPath, public, tmpOut)
	cmd.Dir, cmd.Env = e.Dir, toolEnv(node)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		var msg struct {
			Stage    string  `json:"stage"`
			Progress float64 `json:"progress"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) == nil && progress != nil {
			progress(msg.Stage, msg.Progress)
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("动效渲染失败：%w\n%s", err, tail(stderr.Bytes()))
	}
	return os.Rename(tmpOut, out)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 800 {
		s = "…" + s[len(s)-800:]
	}
	return s
}

// toolEnv puts the tool's own directory first on PATH: apps opened from
// Finder get a minimal PATH, and npm scripts need node next to them.
func toolEnv(bin string) []string {
	path := filepath.Dir(bin) + ":/opt/homebrew/bin:/usr/local/bin:" + os.Getenv("PATH")
	if node, err := lookNode(); err == nil {
		path = filepath.Dir(node) + ":" + path
	}
	return append(os.Environ(), "PATH="+path)
}

func lookNode() (string, error) {
	if p, err := exec.LookPath("node"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, pattern := range []string{"/opt/homebrew/bin/node", "/usr/local/bin/node", filepath.Join(home, ".nvm/versions/node/*/bin/node")} {
		if m, _ := filepath.Glob(pattern); len(m) > 0 {
			return m[len(m)-1], nil
		}
	}
	return "", errors.New("动效需要 Node.js 18 以上，请先安装（brew install node）")
}

func installer() (string, []string, error) {
	home, _ := os.UserHomeDir()
	for _, pattern := range []string{"/opt/homebrew/bin/bun", "/usr/local/bin/bun", filepath.Join(home, ".bun/bin/bun")} {
		if m, _ := filepath.Glob(pattern); len(m) > 0 {
			return m[0], []string{"install", "--no-save"}, nil
		}
	}
	if p, err := exec.LookPath("bun"); err == nil {
		return p, []string{"install", "--no-save"}, nil
	}
	node, err := lookNode()
	if err != nil {
		return "", nil, err
	}
	npm := filepath.Join(filepath.Dir(node), "npm")
	if _, err := os.Stat(npm); err != nil {
		return "", nil, errors.New("没有找到 npm 或 bun，无法安装动效引擎")
	}
	return npm, []string{"install", "--no-audit", "--no-fund"}, nil
}
