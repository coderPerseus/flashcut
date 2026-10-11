package preset

import (
	"strings"
	"testing"

	"flashcut/internal/editor"
	"flashcut/internal/media"
)

func TestBuiltinsValid(t *testing.T) {
	ids := map[string]bool{}
	for _, b := range Builtins() {
		if err := b.Validate(); err != nil {
			t.Errorf("%s: %v", b.Name, err)
		}
		if ids[b.ID] {
			t.Errorf("duplicate id %s", b.ID)
		}
		ids[b.ID] = true
	}
}

func TestValidateCatchesMistakes(t *testing.T) {
	bad := &Template{Name: "x", Questions: []Question{
		{Key: "a b", Label: "a", Type: "noul", Use: editor.ModeKeep, Instructions: "?"},
		{Key: "c", Label: "c", Type: "choice", Use: editor.ModeDrop, Instructions: "?", Options: map[string]string{"x": ""}, Match: []string{"y"}},
		{Key: "d", Label: "d", Type: "score", Use: "explode", Instructions: "?", Levels: []string{"lo", "hi"}, Threshold: 3},
	}}
	err := bad.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{"标识只能用", "2–255 个选项", "不在选项里", "用途", "门槛要在 0–1 级"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func project() *editor.Project {
	p := editor.New("p", media.VideoInfo{Path: "/v.mp4", Duration: 100}, 10)
	p.Edits.PadSeconds = 0
	return p
}

func TestApplyAndChoiceAndOpening(t *testing.T) {
	p := project()
	p.AddRule("保留进球", editor.ModeKeep) // a chat rule survives
	tpl := &Template{Name: "t", FillGaps: false, Questions: []Question{
		{Key: "kill", Label: "击杀", Type: "noul", Use: editor.ModeKeep, Threshold: 0.5, Instructions: "kill?"},
		{Key: "scene", Label: "场景", Type: "choice", Use: editor.ModeDrop, Threshold: 0.6, Instructions: "scene?",
			Options: map[string]string{"game": "", "desk": "", "ad": ""}, Match: []string{"desk", "ad"}},
		{Key: "start", Label: "开局", Type: "noul", Use: editor.ModeOpening, Threshold: 0.5, Instructions: "start?"},
	}}
	Apply(p, tpl)
	if len(p.Edits.Rules) != 4 {
		t.Fatalf("rules %d, want 4", len(p.Edits.Rules))
	}
	ids := map[string]string{}
	for _, r := range p.Edits.Rules {
		ids[r.Text] = r.ID
	}
	for i := range p.Segments {
		s := &p.Segments[i]
		s.Answers[editor.InterestID] = 1
		s.Answers[ids["保留进球"]] = 0
		s.Answers[ids["击杀"]] = 0.1
		s.Answers[ids["开局"]] = 0.1
		s.Choices = map[string]map[string]float64{ids["场景"]: {"game": 0.9, "desk": 0.05, "ad": 0.05}}
	}
	p.Segments[0].Choices[ids["场景"]] = map[string]float64{"game": 0.1, "desk": 0.5, "ad": 0.4} // desk+ad = 0.9: dropped
	p.Segments[0].Answers[ids["击杀"]] = 0.9
	p.Segments[2].Answers[ids["开局"]] = 0.8
	p.Segments[3].Answers[ids["开局"]] = 0.8
	p.Segments[7].Answers[ids["开局"]] = 0.9 // a stray match later is not the opening
	p.Segments[5].Answers[ids["击杀"]] = 0.7
	p.Select()
	var kept []int
	for _, s := range p.Segments {
		if s.Kept {
			kept = append(kept, s.Index)
		}
	}
	if want := []int{2, 3, 5}; len(kept) != 3 || kept[0] != 2 || kept[1] != 3 || kept[2] != 5 {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	// Re-applying keeps the same rule ids, so answers are reused.
	before := ids["击杀"]
	Apply(p, tpl)
	if p.Rule(before) == nil {
		t.Fatal("re-applying changed the rule id")
	}
	if !p.Undo() || p.Rule(before) == nil {
		t.Fatal("undo after re-apply failed")
	}
}

func TestFromProjectRoundTrip(t *testing.T) {
	p := project()
	p.AddRule("只保留进球", editor.ModeKeep)
	Apply(p, Builtins()[0])
	tpl := FromProject(p, "我的模板")
	if err := tpl.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := Export(tpl)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Questions) != len(p.Edits.Rules) {
		t.Fatalf("questions %d, rules %d", len(back.Questions), len(p.Edits.Rules))
	}
	st := Store{Dir: t.TempDir()}
	saved, err := st.Save(back)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.Get(saved.ID); err != nil || got.Name != "我的模板" {
		t.Fatalf("get: %v %v", got, err)
	}
	if err := st.Delete(Builtins()[0].ID); err == nil {
		t.Fatal("deleting a built-in should fail")
	}
	if err := st.Delete(saved.ID); err != nil {
		t.Fatal(err)
	}
}
