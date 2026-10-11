package editor

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"flashcut/internal/clef"
	"flashcut/internal/media"
)

// Intent is how likely a chat message asks for each kind of edit. A
// message can ask for several at once ("cut the replays and keep it under
// five minutes").
type Intent struct {
	Keep     float64 `json:"keep"`
	Drop     float64 `json:"drop"`
	Duration float64 `json:"duration"`
	Range    float64 `json:"range"`
	Export   float64 `json:"export"`
	Undo     float64 `json:"undo"`
	Reset    float64 `json:"reset"`
}

var intentQuestions = map[string]string{
	"keep":     "Does the user ask to keep, include or focus on some kind of content (described by what happens in it, not by timestamps)?",
	"drop":     "Does the user ask to remove, cut or exclude some kind of content (described by what happens in it, not by timestamps)?",
	"duration": "Does the user ask to change the total length of the result (a target length, or shorter / longer)?",
	"range":    "Does the user mention a specific time range or timestamp in the video to keep or remove?",
	"export":   "Does the user ask to export, render, generate or save the final video?",
	"undo":     "Does the user ask to undo or revert the last change?",
	"reset":    "Does the user ask to start over or clear all edits?",
}

// Understand asks Clef-flash what a chat message wants.
func Understand(ctx context.Context, ask Asker, message string) (Intent, int, error) {
	req := clef.Request{
		State:     fmt.Sprintf("A user editing a video in a chat said: “%s”", message),
		Questions: map[string]clef.Question{},
	}
	for id, q := range intentQuestions {
		req.Questions[id] = clef.Noul(q, "", "")
	}
	resp, err := ask.Run(ctx, clef.Flash, req)
	if err != nil {
		return Intent{}, 0, err
	}
	a := resp.Answers
	return Intent{
		Keep: a["keep"].Noul, Drop: a["drop"].Noul, Duration: a["duration"].Noul,
		Range: a["range"].Noul, Export: a["export"].Noul, Undo: a["undo"].Noul, Reset: a["reset"].Noul,
	}, resp.Usage.InputTokens, nil
}

// Outcome is what Apply changed.
type Outcome struct {
	// Reply is the assistant's answer, before any analysis runs.
	Reply string `json:"reply"`
	// Analyze is true when new rules need the model to look at the video.
	Analyze bool `json:"analyze"`
	// Export is true when the user asked to export.
	Export bool `json:"export"`
	// Changed is true when the edits changed.
	Changed bool `json:"changed"`
}

// Apply turns an understood chat message into edits.
func (p *Project) Apply(message string, in Intent) Outcome {
	var out Outcome
	var done []string
	msg := strings.TrimSpace(message)

	switch {
	case in.Reset >= 0.8:
		p.Checkpoint()
		p.Edits.Rules, p.Edits.Ranges, p.Edits.TargetSeconds = nil, nil, 0
		p.Edits.Overrides = map[int]bool{}
		out.Changed = true
		out.Reply = "已清空所有剪辑要求，回到按精彩程度自动挑选。"
		return out
	case in.Undo >= 0.7:
		if p.Undo() {
			out.Changed = true
			out.Reply = "已撤销上一步修改。"
		} else {
			out.Reply = "没有可以撤销的修改了。"
		}
		return out
	}

	before := p.Edits.clone()
	changed := false

	// Exact time ranges.
	rangeDone := false
	if spans := ParseRanges(msg, p.Source.Duration); len(spans) > 0 && (in.Range >= 0.4 || in.Keep >= 0.5 || in.Drop >= 0.5) {
		keep := !wantsRemoval(msg)
		for _, s := range spans {
			p.Edits.Ranges = append(p.Edits.Ranges, RangeRule{Start: s.Start, End: s.End, Keep: keep})
			verb := "保留"
			if !keep {
				verb = "去掉"
			}
			done = append(done, fmt.Sprintf("%s %s–%s", verb, media.Clock(s.Start), media.Clock(s.End)))
		}
		changed, rangeDone = true, true
	}

	// Length.
	durDone := false
	if in.Duration >= 0.5 {
		current := p.Summarize().KeptSeconds
		sec, ok := ParseDuration(msg, current)
		switch {
		case rangeDone:
		case asksLonger(msg) && p.hasKeepRules():
			// Keep rules decide what is kept, so a longer cut means
			// matching more loosely rather than raising a cap.
			for i := range p.Edits.Rules {
				if r := &p.Edits.Rules[i]; r.Mode == ModeKeep {
					r.Threshold = math.Max(0.1, r.Threshold-0.1)
				}
			}
			if p.Edits.TargetSeconds > 0 && p.Edits.TargetSeconds < sec {
				p.Edits.TargetSeconds = sec
			}
			done = append(done, "放宽保留要求，多留一些片段")
			changed, durDone = true, true
		case asksLonger(msg) && p.Edits.TargetSeconds == 0:
			p.Edits.InterestThreshold = math.Max(0.5, p.Edits.InterestThreshold-0.4)
			done = append(done, "降低精彩程度门槛，多留一些片段")
			changed, durDone = true, true
		case asksLonger(msg):
			p.Edits.TargetSeconds = math.Min(p.Source.Duration, p.Edits.TargetSeconds*1.3)
			done = append(done, "时长上限放宽到 "+media.Clock(p.Edits.TargetSeconds))
			changed, durDone = true, true
		case ok:
			sec = math.Max(5, math.Min(sec, p.Source.Duration))
			p.Edits.TargetSeconds = sec
			done = append(done, "时长控制在 "+media.Clock(sec)+" 以内")
			changed, durDone = true, true
		default:
			out.Reply = "想剪到多长？比如“控制在 3 分钟”或“再短一点”。"
		}
	}

	// Content rules, unless the message was only about time.
	if !rangeDone {
		keep, drop := in.Keep >= 0.5, in.Drop >= 0.5
		// "Shorter" reads as removing something; only trust it when strong.
		if durDone && in.Keep < 0.5 && in.Drop < 0.85 {
			keep, drop = false, false
		}
		if keep || drop {
			mode := ModeKeep
			if drop && (!keep || in.Drop > in.Keep) {
				mode = ModeDrop
			}
			r := p.AddRule(msg, mode)
			if mode == ModeKeep {
				done = append(done, "按要求保留：“"+r.Text+"”")
			} else {
				done = append(done, "按要求去掉：“"+r.Text+"”")
			}
			changed = true
			out.Analyze = true
		}
	}

	if in.Export >= 0.5 {
		out.Export = true
		done = append(done, "导出成片")
	}

	if changed {
		p.History = append(p.History, before)
		out.Changed = true
	}
	if len(done) > 0 {
		out.Reply = "好的：" + strings.Join(done, "；") + "。"
	} else if out.Reply == "" {
		out.Reply = "我可以按你的要求剪辑，比如：\n· 只保留进球和庆祝\n· 去掉片头广告\n· 3:20 到 4:10 不要\n· 控制在 3 分钟以内\n· 撤销 / 重来 / 导出"
	}
	return out
}

func asksLonger(msg string) bool {
	return containsAny(msg, "长一点", "长点", "更长", "再长", "多留", "多保留", "longer")
}

func (p *Project) hasKeepRules() bool {
	for _, r := range p.Edits.Rules {
		if r.Enabled && r.Mode == ModeKeep {
			return true
		}
	}
	return false
}

func wantsRemoval(msg string) bool {
	for _, w := range []string{"不要", "去掉", "删", "剪掉", "移除", "去除", "砍掉", "跳过", "cut", "remove", "delete", "drop", "skip"} {
		if strings.Contains(strings.ToLower(msg), w) {
			return true
		}
	}
	return false
}

// AddMessage appends a chat message.
func (p *Project) AddMessage(role, text string) {
	p.Chat = append(p.Chat, Message{Role: role, Text: text, At: time.Now()})
}

var (
	cnDigits = map[rune]float64{'零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}

	reDur    = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?|[零一二两三四五六七八九十百半]+)\s*(个)?\s*(小时|h|hours?|分钟|分|min(?:ute)?s?|m|秒钟|秒|s(?:ec(?:ond)?s?)?)\s*(半)?`)
	reClock  = regexp.MustCompile(`\b(\d{1,2}):(\d{2})(?::(\d{2}))?\b`)
	reCNTime = regexp.MustCompile(`(\d+)\s*分\s*(?:(\d+)\s*秒)?`)
	reSep    = regexp.MustCompile(`到|至|~|～|-|—|－`)
	reHead   = regexp.MustCompile(`(?i)(?:开头|片头|最前面|前面|前|first)\s*的?\s*` + reDur.String())
	reTail   = regexp.MustCompile(`(?i)(?:最后|结尾|片尾|末尾|last)\s*的?\s*` + reDur.String())
)

// cnNumber parses 0–999 in Chinese numerals ("十五", "两百", "半").
func cnNumber(s string) (float64, bool) {
	if s == "半" {
		return 0.5, true
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v, true
	}
	total, cur := 0.0, 0.0
	for _, r := range s {
		switch {
		case r == '十':
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
		case r == '百':
			total += cur * 100
			cur = 0
		default:
			d, ok := cnDigits[r]
			if !ok {
				return 0, false
			}
			cur = d
		}
	}
	return total + cur, true
}

// ParseDuration reads a target length from msg: "3分钟", "2分30秒",
// "90s", "一分半", or a relative "短一点" / "长一点" against current.
func ParseDuration(msg string, current float64) (float64, bool) {
	total, found := 0.0, false
	for _, m := range reDur.FindAllStringSubmatch(msg, -1) {
		n, ok := cnNumber(m[1])
		if !ok {
			continue
		}
		unit := strings.ToLower(m[3])
		switch {
		case unit == "小时" || unit == "h" || strings.HasPrefix(unit, "hour"):
			n *= 3600
			if m[4] == "半" {
				n += 1800
			}
		case unit == "分钟" || unit == "分" || unit == "m" || strings.HasPrefix(unit, "min"):
			n *= 60
			if m[4] == "半" {
				n += 30
			}
		}
		total += n
		found = true
	}
	if found && total > 0 {
		return total, true
	}
	if current <= 0 {
		return 0, false
	}
	switch {
	case containsAny(msg, "短一点", "短点", "更短", "再短", "缩短", "精简", "shorter"):
		return current * 0.7, true
	case asksLonger(msg):
		return current * 1.3, true
	case containsAny(msg, "一半"):
		return current * 0.5, true
	}
	return 0, false
}

// ParseRanges reads time ranges from msg: "3:20到4:10", "1:02:03-1:05:00",
// "3分20秒到4分", "开头30秒", "最后1分钟", "10:00之后".
func ParseRanges(msg string, videoLen float64) []media.Range {
	var out []media.Range
	// Clock times, paired in order.
	var times []float64
	for _, m := range reClock.FindAllStringSubmatch(msg, -1) {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		t := float64(a*60 + b)
		if m[3] != "" {
			c, _ := strconv.Atoi(m[3])
			t = float64(a*3600 + b*60 + c)
		}
		times = append(times, t)
	}
	if len(times) == 0 && reSep.MatchString(msg) {
		for _, m := range reCNTime.FindAllStringSubmatch(msg, -1) {
			a, _ := strconv.Atoi(m[1])
			b, _ := strconv.Atoi(m[2])
			times = append(times, float64(a*60+b))
		}
	}
	for i := 0; i+1 < len(times); i += 2 {
		if times[i+1] > times[i] {
			out = append(out, media.Range{Start: times[i], End: times[i+1]})
		}
	}
	if len(times) == 1 {
		switch {
		case containsAny(msg, "之后", "以后", "开始", "往后", "after"):
			out = append(out, media.Range{Start: times[0], End: videoLen})
		case containsAny(msg, "之前", "以前", "before"):
			out = append(out, media.Range{Start: 0, End: times[0]})
		}
	}
	if len(out) > 0 {
		return out
	}
	// "开头30秒" / "最后1分钟": the place word right before the length.
	if m := reHead.FindString(msg); m != "" {
		if d, ok := ParseDuration(m, 0); ok {
			return []media.Range{{Start: 0, End: math.Min(d, videoLen)}}
		}
	}
	if m := reTail.FindString(msg); m != "" {
		if d, ok := ParseDuration(m, 0); ok {
			return []media.Range{{Start: math.Max(0, videoLen-d), End: videoLen}}
		}
	}
	return nil
}

func containsAny(s string, words ...string) bool {
	low := strings.ToLower(s)
	for _, w := range words {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}
