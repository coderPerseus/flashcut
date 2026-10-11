package subs

import (
	"strings"
	"testing"
)

const srt = "\uFEFF1\r\n00:00:01,000 --> 00:00:03,500\r\n今天我们来看比塞\r\n\r\n2\r\n00:00:04,000 --> 00:00:06,000\r\n<i>第二行</i>\r\n两行字幕\r\n\r\n"

func TestSRTRoundTrip(t *testing.T) {
	f, err := Parse("a.srt", []byte(srt))
	if err != nil {
		t.Fatal(err)
	}
	if f.Format != SRT || len(f.Cues) != 2 || f.Cues[0].Start != 1 || f.Cues[0].End != 3.5 || f.Cues[1].Plain() != "第二行\n两行字幕" {
		t.Fatalf("parsed %+v", f.Cues)
	}
	out := string(f.Write(map[int]string{1: "今天我们来看比赛"}))
	if !strings.Contains(out, "00:00:01,000 --> 00:00:03,500\n今天我们来看比赛") || !strings.Contains(out, "<i>第二行</i>") {
		t.Fatalf("written:\n%s", out)
	}
}

func TestVTT(t *testing.T) {
	vtt := "WEBVTT\nKind: captions\n\n00:01.000 --> 00:02.500 align:start\nhello\n\n01:00:02.000 --> 01:00:03.000\nworld\n"
	f, err := Parse("a.vtt", []byte(vtt))
	if err != nil {
		t.Fatal(err)
	}
	if f.Format != VTT || len(f.Cues) != 2 || f.Cues[0].Start != 1 || f.Cues[1].Start != 3602 {
		t.Fatalf("parsed %+v", f.Cues)
	}
	out := string(f.Write(map[int]string{2: "word"}))
	if !strings.HasPrefix(out, "WEBVTT\nKind: captions\n\n") || !strings.Contains(out, "01:00:02.000 --> 01:00:03.000\nword") {
		t.Fatalf("written:\n%s", out)
	}
}

func TestASS(t *testing.T) {
	ass := "[Script Info]\nTitle: x\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:01.50,0:00:03.00,Default,,0,0,0,,{\\b1}你好,世界\\N第二行\n"
	f, err := Parse("a.ass", []byte(ass))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Cues) != 1 || f.Cues[0].Start != 1.5 || f.Cues[0].Plain() != "你好,世界\n第二行" {
		t.Fatalf("parsed %+v", f.Cues)
	}
	out := string(f.Write(map[int]string{1: "{\\b1}你好，世界\n第二行"}))
	if !strings.Contains(out, "Dialogue: 0,0:00:01.50,0:00:03.00,Default,,0,0,0,,{\\b1}你好，世界\\N第二行") || !strings.Contains(out, "[Script Info]") {
		t.Fatalf("written:\n%s", out)
	}
}

func TestNormalizeAndJSON(t *testing.T) {
	if normalize("你好，世界！") != normalize("你好 世界") {
		t.Fatal("punctuation should not matter")
	}
	if Similarity("要吃龙吗", "要吃饭吗") != 0.75 || Similarity("你好", "你好！") != 1 {
		t.Fatal("similarity")
	}
	if got := firstJSON("sure: {\"a\": 1} done"); got != `{"a": 1}` {
		t.Fatalf("firstJSON = %q", got)
	}
}
