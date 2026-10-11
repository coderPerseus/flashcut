package preset

import (
	"flashcut/internal/editor"
	"flashcut/internal/media"
)

// Builtins are the templates that ship with the app.
func Builtins() []*Template {
	out := []*Template{esports(), sports(), interview(), vlog(), course()}
	for _, t := range out {
		t.Version, t.Builtin = FormatVersion, true
	}
	return out
}

func noul(key, label string, use editor.Mode, threshold float64, instructions, yes, no string) Question {
	return Question{Key: key, Label: label, Type: "noul", Use: use, Threshold: threshold, Instructions: instructions, Yes: yes, No: no}
}

func esports() *Template {
	kill := noul("kill", "击杀", editor.ModeKeep, 0.4,
		"Is at least one champion or player killed in this clip (a kill announcement, a new entry in the kill feed, or a character dying on screen)?",
		"A kill happens, including first blood, executes and shutdowns", "Nobody dies in this clip")
	kill.Marker = "highlight"
	fight := noul("teamfight", "团战", editor.ModeKeep, 0.6,
		"Is a large fight between the two teams happening in this clip?",
		"Several players from both teams are fighting", "No big fight")
	fight.Marker = "highlight"
	objective := noul("objective", "资源争夺", editor.ModeKeep, 0.6,
		"Is a major objective being taken in this clip (dragon, baron, herald, a tower or an inhibitor, or the equivalent in this game)?",
		"An objective is taken or fought over", "No objective")
	objective.Marker = "transition"
	opening := noul("opening", "开局", editor.ModeOpening, 0.5,
		"Is this the opening of the game: the in-game match has just started (game timer near 0:00, players leaving their base, or the broadcast moving from the draft into the game)?",
		"The first moments of the actual game", "Draft, desk, or the game is already underway")
	opening.Marker = "opening"
	ending := noul("ending", "结尾", editor.ModeEnding, 0.5,
		"Is this the ending of the game: the final push, the base exploding, or the victory or defeat screen?",
		"The game is being won or has just been won", "The game is still going on, or this is a break or desk segment")
	ending.Marker = "ending"
	return &Template{
		ID: "builtin-esports", Name: "电竞 · 英雄联盟", Category: "电竞",
		Description: "保留每次击杀、团战、资源争夺，以及开局和结尾；去掉回放、BP 和解说台。适合 LOL、王者荣耀、DOTA 等比赛录像。",
		Questions: []Question{
			kill, fight, objective, opening, ending,
			noul("replay", "回放", editor.ModeDrop, 0.7,
				"Is this clip an instant replay or slow-motion replay of an earlier play (usually marked REPLAY or shown with a transition)?",
				"A replay", "Live gameplay"),
			{
				Key: "scene", Label: "BP / 解说台 / 广告", Type: "choice", Use: editor.ModeDrop, Threshold: 0.6,
				Instructions: "What does this clip mainly show?",
				Options: map[string]string{
					"game":  "live gameplay",
					"draft": "champion select, bans and picks",
					"desk":  "casters, players, crowd or the analysis desk",
					"break": "an ad, a sponsor spot, a break or a waiting screen",
				},
				Match: []string{"draft", "desk", "break"},
			},
		},
		SegmentSeconds: 15, PadSeconds: 2, FillGaps: true, InterestThreshold: 2.5,
		Effects:  effects("esports", "#7c5cff"),
		Examples: []string{"也保留所有推塔", "控制在 5 分钟以内", "去掉大龙之前的部分"},
	}
}

func sports() *Template {
	score := noul("score", "得分 / 进球", editor.ModeKeep, 0.45,
		"Does someone score in this clip (a goal, a basket, a try, a point won), or is a score shown changing?",
		"A score happens", "No score")
	score.Marker = "highlight"
	chance := noul("chance", "精彩攻防", editor.ModeKeep, 0.65,
		"Is there a dangerous attack, a great save, a big hit or an outstanding play in this clip?",
		"A big moment of play", "Routine play")
	chance.Marker = "highlight"
	celebration := noul("celebration", "庆祝", editor.ModeBoost, 0.6,
		"Are players or fans celebrating in this clip?", "Celebration", "No celebration")
	opening := noul("opening", "开场", editor.ModeOpening, 0.5,
		"Is this the start of play: kick-off, tip-off, the first serve, or the referee starting the match?",
		"Play is just starting", "Play is already underway or has not started")
	opening.Marker = "opening"
	ending := noul("ending", "终场", editor.ModeEnding, 0.5,
		"Is this the end of the match: the final whistle, the final score shown, or players reacting to the result?",
		"The match is ending or has just ended", "The match is still going on")
	ending.Marker = "ending"
	return &Template{
		ID: "builtin-sports", Name: "体育比赛", Category: "体育",
		Description: "保留进球得分、精彩攻防、开场和终场，去掉回放、中场和广告。",
		Questions: []Question{
			score, chance, celebration, opening, ending,
			noul("replay", "回放", editor.ModeDrop, 0.7, "Is this clip a replay or slow-motion replay of an earlier moment?", "A replay", "Live play"),
			noul("break", "中场 / 广告", editor.ModeDrop, 0.6, "Is this clip a break in play: halftime, studio talk, an ad, or a waiting screen?", "A break", "Play or the field"),
		},
		SegmentSeconds: 15, PadSeconds: 3, FillGaps: true, InterestThreshold: 2.5,
		Effects:  effects("esports", "#00c2ff"),
		Examples: []string{"只要进球", "控制在 3 分钟以内", "去掉中场休息"},
	}
}

func interview() *Template {
	point := noul("point", "观点 / 金句", editor.ModeKeep, 0.6,
		"In this clip, does a speaker make a clear, memorable point: a strong opinion, a key insight, a story's punchline, or a quotable line?",
		"A memorable point is made", "Small talk, setup, or filler")
	point.Marker = "highlight"
	emotion := noul("emotion", "笑点 / 情绪", editor.ModeBoost, 0.6,
		"Is there laughter, a strong emotion, or a lively exchange in this clip?", "Laughter or emotion", "Calm talk")
	emotion.Marker = "highlight"
	return &Template{
		ID: "builtin-interview", Name: "访谈 / 播客", Category: "对话",
		Description: "保留观点、金句和笑点，去掉寒暄、冷场、广告和技术问题。适合采访、播客、直播切片。",
		Questions: []Question{
			point, emotion,
			noul("dead", "冷场 / 寒暄", editor.ModeDrop, 0.65,
				"Is this clip mostly silence, greetings, setup, technical problems, or off-topic chatter?", "Nothing worth keeping", "Real content"),
			noul("ad", "广告口播", editor.ModeDrop, 0.7, "Is someone reading an ad or sponsor message in this clip?", "An ad", "No ad"),
			{
				Key: "topic_shift", Label: "话题切换", Type: "noul", Use: editor.ModeMarker, Threshold: 0.6,
				Instructions: "Does the conversation move to a new topic or question in this clip?",
				Marker:       "transition",
			},
		},
		Effects:        effects("clean", "#ff8a3d"),
		SegmentSeconds: 20, PadSeconds: 1, FillGaps: true, InterestThreshold: 2.5,
		Examples: []string{"只保留讲到 AI 的部分", "剪成 90 秒的短视频", "去掉主持人的开场白"},
	}
}

func vlog() *Template {
	scene := noul("scenic", "好看的画面", editor.ModeKeep, 0.6,
		"Is this clip visually striking: a beautiful view, an interesting place, or a well-composed shot?", "A striking shot", "Ordinary footage")
	scene.Marker = "transition"
	return &Template{
		ID: "builtin-vlog", Name: "Vlog / 旅行", Category: "生活",
		Description: "保留好看的画面和有内容的讲述，去掉晃动、模糊、赶路和无效镜头。",
		Questions: []Question{
			scene,
			noul("talk", "有内容的讲述", editor.ModeKeep, 0.6,
				"Is someone talking to the camera and actually saying something interesting in this clip?", "Interesting talk", "No talk or empty talk"),
			{
				Key: "quality", Label: "画面质量", Type: "score", Use: editor.ModeBoost, Threshold: 2,
				Instructions: "How good is the footage quality in this clip?",
				Levels:       []string{"Unusable: shaky, blurry, dark or pointing at nothing", "Poor", "Okay", "Good", "Excellent: steady, sharp, well lit"},
			},
			noul("junk", "无效镜头", editor.ModeDrop, 0.7,
				"Is this clip unusable: very shaky, blurry, accidental recording, or walking with nothing happening?", "Unusable", "Usable"),
		},
		Effects:        effects("clean", "#ff8a3d"),
		SegmentSeconds: 10, PadSeconds: 0.5, FillGaps: true, InterestThreshold: 2.5, Quality: media.QualityMedium,
		Examples: []string{"保留吃东西的镜头", "剪成 1 分钟", "去掉在车上的部分"},
	}
}

func course() *Template {
	key := noul("key", "重点讲解", editor.ModeKeep, 0.55,
		"Does this clip explain an important concept, show a key step or demo, or give a conclusion?", "Key teaching content", "Not key content")
	key.Marker = "transition"
	return &Template{
		ID: "builtin-course", Name: "课程 / 教程", Category: "知识",
		Description: "保留重点讲解、操作演示和总结，去掉等待、跑题和重复。",
		Questions: []Question{
			key,
			noul("demo", "操作演示", editor.ModeKeep, 0.6, "Is someone demonstrating something step by step on screen in this clip?", "A demo", "No demo"),
			noul("summary", "总结", editor.ModeBoost, 0.6, "Is this clip a summary or recap of what was covered?", "A summary", "Not a summary"),
			noul("idle", "等待 / 跑题", editor.ModeDrop, 0.65,
				"Is this clip mostly waiting, setup, silence, off-topic talk, or repeating something already said?", "Idle or off-topic", "Teaching"),
		},
		Effects:        effects("clean", "#ff8a3d"),
		SegmentSeconds: 20, PadSeconds: 1, FillGaps: true, InterestThreshold: 2.5,
		Examples: []string{"只保留讲到公式的部分", "控制在 10 分钟", "去掉开头自我介绍"},
	}
}

func effects(style, accent string) *editor.EffectSettings {
	e := editor.DefaultEffects()
	e.Style, e.Accent = style, accent
	return &e
}
