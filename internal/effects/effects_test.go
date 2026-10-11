package effects

import (
	"testing"

	"flashcut/internal/clef"
	"flashcut/internal/editor"
	"flashcut/internal/media"
)

func TestBuildPlan(t *testing.T) {
	p := editor.New("p", media.VideoInfo{Path: "/m/GEN vs HLE.mp4", Duration: 100}, 10)
	p.Edits.PadSeconds, p.Edits.FillGaps = 0, false
	q := clef.Noul("kill?", "", "")
	p.Edits.Rules = []editor.Rule{
		{ID: "kill", Text: "击杀", Mode: editor.ModeKeep, Threshold: 0.5, Enabled: true, Question: &q, Marker: "highlight"},
		{ID: "end", Text: "结尾", Mode: editor.ModeEnding, Threshold: 0.5, Enabled: true, Question: &q, Marker: "ending"},
	}
	for i := range p.Segments {
		s := &p.Segments[i]
		s.Answers[editor.InterestID] = 2
		s.Answers["kill"] = 0
		s.Answers["end"] = 0
	}
	// Kept: 20–30 (kill), 50–70 (kill at 60), 90–100 (ending).
	p.Segments[2].Answers["kill"] = 0.9
	p.Segments[5].Answers["kill"] = 0.6
	p.Segments[6].Answers["kill"] = 0.9
	p.Segments[9].Answers["end"] = 0.9
	p.Select()
	if got := len(p.Ranges()); got != 3 {
		t.Fatalf("ranges %v", p.Ranges())
	}
	s := editor.DefaultEffects()
	s.Style = "esports"
	plan := BuildPlan(p, s, media.VideoInfo{Width: 1281, Height: 720, Duration: 40})
	if plan.Title != "GEN vs HLE" || plan.Width != 1280 || plan.OutroSeconds == 0 {
		t.Fatalf("plan header %+v", plan)
	}
	want := []Event{
		{Kind: "highlight", Start: 0, End: 10, Label: "击杀"},
		{Kind: "highlight", Start: 10, End: 30, Label: "击杀"}, // two kill segments merge
		{Kind: "transition", Start: 10, End: 10},
		{Kind: "ending", Start: 30, End: 40, Label: "终局时刻"},
		{Kind: "transition", Start: 30, End: 30},
	}
	if len(plan.Events) != len(want) {
		t.Fatalf("events %+v", plan.Events)
	}
	for _, w := range want {
		found := false
		for _, e := range plan.Events {
			if e == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %+v in %+v", w, plan.Events)
		}
	}
}
