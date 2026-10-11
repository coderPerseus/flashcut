package editor

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"flashcut/internal/clef"
	"flashcut/internal/media"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestParseDuration(t *testing.T) {
	cases := []struct {
		msg  string
		cur  float64
		want float64
		ok   bool
	}{
		{"控制在5分钟以内", 0, 300, true},
		{"剪成 2分30秒", 0, 150, true},
		{"一分半就够了", 0, 90, true},
		{"十五分钟", 0, 900, true},
		{"90s", 0, 90, true},
		{"3 min", 0, 180, true},
		{"再短一点", 600, 420, true},
		{"长一点", 100, 130, true},
		{"只要一半", 600, 300, true},
		{"保留击杀", 600, 0, false},
	}
	for _, c := range cases {
		got, ok := ParseDuration(c.msg, c.cur)
		if ok != c.ok || (ok && !near(got, c.want)) {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v, %v", c.msg, got, ok, c.want, c.ok)
		}
	}
}

func TestParseRanges(t *testing.T) {
	const L = 2860
	cases := []struct {
		msg  string
		want []media.Range
	}{
		{"3:20到4:10这段不要", []media.Range{{Start: 200, End: 250}}},
		{"保留 1:02:03-1:05:00", []media.Range{{Start: 3723, End: 3900}}},
		{"3分20秒到4分不要", []media.Range{{Start: 200, End: 240}}},
		{"开头30秒去掉", []media.Range{{Start: 0, End: 30}}},
		{"最后1分钟保留", []media.Range{{Start: L - 60, End: L}}},
		{"10:00之后都不要", []media.Range{{Start: 600, End: L}}},
		{"去掉前面的广告，控制在5分钟以内", nil},
		{"只保留击杀", nil},
	}
	for _, c := range cases {
		got := ParseRanges(c.msg, L)
		if len(got) != len(c.want) {
			t.Errorf("ParseRanges(%q) = %v; want %v", c.msg, got, c.want)
			continue
		}
		for i := range got {
			if !near(got[i].Start, c.want[i].Start) || !near(got[i].End, c.want[i].End) {
				t.Errorf("ParseRanges(%q) = %v; want %v", c.msg, got, c.want)
			}
		}
	}
}

func testProject() *Project {
	p := New("t", media.VideoInfo{Path: "/v.mp4", Duration: 100}, 10)
	p.Edits.PadSeconds = 0
	for i := range p.Segments {
		p.Segments[i].Answers[InterestID] = float64(i % 5) // 0,1,2,3,4,0,1,2,3,4
	}
	return p
}

func TestSelectByInterest(t *testing.T) {
	p := testProject()
	p.Edits.FillGaps = false
	p.Select()
	var kept []int
	for _, s := range p.Segments {
		if s.Kept {
			kept = append(kept, s.Index)
		}
	}
	// Levels ≥ 2.5: indexes 3,4,8,9.
	if want := []int{3, 4, 8, 9}; !equal(kept, want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	if got := p.Summarize().KeptSeconds; !near(got, 40) {
		t.Fatalf("kept %vs, want 40", got)
	}
}

func TestSelectRulesAndTarget(t *testing.T) {
	p := testProject()
	p.Edits.FillGaps = false
	keep := p.AddRule("只保留进球", ModeKeep)
	drop := p.AddRule("去掉回放", ModeDrop)
	for i := range p.Segments {
		p.Segments[i].Answers[keep.ID] = 0.1
		p.Segments[i].Answers[drop.ID] = 0.1
	}
	p.Segments[1].Answers[keep.ID] = 0.9
	p.Segments[2].Answers[keep.ID] = 0.7
	p.Segments[6].Answers[keep.ID] = 0.95
	p.Segments[6].Answers[drop.ID] = 0.8 // a replay of a goal: drop wins
	p.Segments[7].Answers[keep.ID] = 0.6
	p.Select()
	if got := keptIndexes(p); !equal(got, []int{1, 2, 7}) {
		t.Fatalf("kept %v, want [1 2 7]", got)
	}
	if !strings.HasPrefix(p.Segments[6].Reason, "去掉") {
		t.Fatalf("segment 6 reason %q", p.Segments[6].Reason)
	}
	// A 20 s cap keeps the two most likely matches.
	p.Edits.TargetSeconds = 20
	p.Select()
	if got := keptIndexes(p); !equal(got, []int{1, 2}) {
		t.Fatalf("with target kept %v, want [1 2]", got)
	}
}

func TestRangesAndUndo(t *testing.T) {
	p := testProject()
	p.Edits.FillGaps = false
	p.Select() // keeps 30–50 and 80–100
	p.Checkpoint()
	p.Edits.Ranges = []RangeRule{{Start: 35, End: 45, Keep: false}, {Start: 60, End: 62.5, Keep: true}}
	got := p.Ranges()
	want := []media.Range{{Start: 30, End: 35}, {Start: 45, End: 50}, {Start: 60, End: 62.5}, {Start: 80, End: 100}}
	if len(got) != len(want) {
		t.Fatalf("ranges %v, want %v", got, want)
	}
	for i := range want {
		if !near(got[i].Start, want[i].Start) || !near(got[i].End, want[i].End) {
			t.Fatalf("ranges %v, want %v", got, want)
		}
	}
	if !p.Undo() || len(p.Edits.Ranges) != 0 {
		t.Fatal("undo did not restore the edits")
	}
}

func TestPadding(t *testing.T) {
	p := testProject()
	p.Edits.FillGaps = false
	p.Edits.PadSeconds = 2
	p.Select() // segments 30–50 and 80–100
	got := p.Ranges()
	if len(got) != 2 || !near(got[0].Start, 28) || !near(got[0].End, 52) || !near(got[1].Start, 78) || !near(got[1].End, 100) {
		t.Fatalf("padded ranges %v", got)
	}
}

func TestTargetCountsPadding(t *testing.T) {
	p := testProject()
	p.Edits.PadSeconds = 2
	p.Edits.TargetSeconds = 30
	p.Select()
	if got := p.keptSeconds(); got > 30.5 {
		t.Fatalf("kept %vs with a 30s target", got)
	}
}

func TestLongerLoosensKeepRules(t *testing.T) {
	p := testProject()
	r := p.AddRule("保留进球", ModeKeep)
	for i := range p.Segments {
		p.Segments[i].Answers[r.ID] = 0.3
	}
	p.Select()
	p.Apply("再长一点", Intent{Duration: 0.9})
	if got := p.Rule(r.ID).Threshold; !near(got, 0.25) {
		t.Fatalf("threshold %v, want 0.25", got)
	}
	if p.Edits.TargetSeconds != 0 {
		t.Fatalf("longer should not set a cap, got %v", p.Edits.TargetSeconds)
	}
}

func TestApply(t *testing.T) {
	p := testProject()
	p.Select()
	out := p.Apply("把所有回放都去掉，然后控制在5分钟以内", Intent{Drop: 0.91, Duration: 0.95})
	if !out.Analyze || len(p.Edits.Rules) != 1 || p.Edits.Rules[0].Mode != ModeDrop || !near(p.Edits.TargetSeconds, 100) {
		t.Fatalf("apply: %+v rules=%+v target=%v", out, p.Edits.Rules, p.Edits.TargetSeconds)
	}
	out = p.Apply("再短一点", Intent{Drop: 0.68, Duration: 0.94})
	if out.Analyze || len(p.Edits.Rules) != 1 {
		t.Fatalf("shorter should not add a rule: %+v", out)
	}
	out = p.Apply("3:20到4:10这段不要", Intent{Drop: 0.64, Range: 0.97})
	if out.Analyze || len(p.Edits.Ranges) != 1 || p.Edits.Ranges[0].Keep {
		t.Fatalf("range: %+v %+v", out, p.Edits.Ranges)
	}
	out = p.Apply("只保留击杀和团战", Intent{Keep: 0.94, Drop: 0.84})
	if p.Edits.Rules[len(p.Edits.Rules)-1].Mode != ModeKeep {
		t.Fatal("only-keep should become a keep rule")
	}
	n := len(p.Edits.Rules)
	if out = p.Apply("撤销", Intent{Undo: 0.95}); len(p.Edits.Rules) != n-1 {
		t.Fatalf("undo: %+v", out)
	}
	if out = p.Apply("导出吧", Intent{Export: 0.91}); !out.Export || out.Changed {
		t.Fatalf("export: %+v", out)
	}
	if out = p.Apply("这个软件怎么用？", Intent{}); out.Changed || !strings.Contains(out.Reply, "比如") {
		t.Fatalf("help: %+v", out)
	}
}

type fakeAsker struct{ calls atomic.Int64 }

func (f *fakeAsker) Run(_ context.Context, model string, req clef.Request) (*clef.Response, error) {
	f.calls.Add(1)
	r := &clef.Response{Answers: map[string]clef.Answer{}}
	for id, q := range req.Questions {
		if q.Type == "score" {
			r.Answers[id] = clef.Answer{Type: "score", Score: 3}
		} else {
			r.Answers[id] = clef.Answer{Type: "noul", Noul: 0.8}
		}
	}
	r.Usage.InputTokens = 100
	return r, nil
}

func TestAnalyzeOnlyAsksPending(t *testing.T) {
	if _, err := media.LookTool("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	src := t.TempDir() + "/v.mp4"
	if err := makeTestVideo(src); err != nil {
		t.Fatal(err)
	}
	info, err := media.Probe(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	p := New("t", info, 5)
	f := &fakeAsker{}
	if err := p.Analyze(context.Background(), f, t.TempDir(), 2, nil, nil); err != nil {
		t.Fatal(err)
	}
	if int(f.calls.Load()) != len(p.Segments) || len(p.Pending()) != 0 {
		t.Fatalf("calls %d for %d segments, pending %v", f.calls.Load(), len(p.Segments), p.Pending())
	}
	p.AddRule("保留有人说话的部分", ModeKeep)
	f.calls.Store(0)
	if err := p.Analyze(context.Background(), f, t.TempDir(), 2, nil, nil); err != nil {
		t.Fatal(err)
	}
	if int(f.calls.Load()) != len(p.Segments) || p.Tokens != 200*len(p.Segments) {
		t.Fatalf("second pass: calls %d, tokens %d", f.calls.Load(), p.Tokens)
	}
}

func keptIndexes(p *Project) []int {
	var out []int
	for _, s := range p.Segments {
		if s.Kept {
			out = append(out, s.Index)
		}
	}
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
