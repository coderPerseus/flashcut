package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo"

	"flashcut/internal/clef"
	"flashcut/internal/editor"
	"flashcut/internal/llm"
	"flashcut/internal/media"
	"flashcut/internal/preset"
)

var agentTools = []llm.Tool{
	{Name: "keep_content", Description: "Keep the parts of the video that show something. With any keep request, only matching parts are kept. Every segment is checked by a video model, which takes a few minutes.",
		Parameters: obj(map[string]any{"description": str("What to keep, in the user's own words, e.g. 所有击杀和团战")}, "description")},
	{Name: "remove_content", Description: "Cut out the parts of the video that show something. Every segment is checked by a video model, which takes a few minutes.",
		Parameters: obj(map[string]any{"description": str("What to remove, in the user's own words, e.g. 回放")}, "description")},
	{Name: "smart_cut", Description: "Pick the most worth-watching parts automatically, without a specific request.", Parameters: obj(nil)},
	{Name: "set_length", Description: "Cap the length of the result. Use 0 to remove the cap.",
		Parameters: obj(map[string]any{"seconds": num("Maximum length in seconds")}, "seconds")},
	{Name: "change_length", Description: "Make the result shorter or longer than now.",
		Parameters: obj(map[string]any{"direction": map[string]any{"type": "string", "enum": []string{"shorter", "longer"}}}, "direction")},
	{Name: "keep_range", Description: "Always keep an exact time range of the original video.",
		Parameters: obj(map[string]any{"start": str("Start time, e.g. 3:20 or 200"), "end": str("End time, e.g. 4:10")}, "start", "end")},
	{Name: "remove_range", Description: "Always cut an exact time range of the original video.",
		Parameters: obj(map[string]any{"start": str("Start time, e.g. 3:20 or 200"), "end": str("End time, e.g. 4:10")}, "start", "end")},
	{Name: "apply_template", Description: "Apply an editing template by name, such as 电竞 · 英雄联盟, 体育比赛, 访谈 / 播客, Vlog / 旅行, 课程 / 教程.",
		Parameters: obj(map[string]any{"name": str("Template name")}, "name")},
	{Name: "undo", Description: "Undo the last edit.", Parameters: obj(nil)},
	{Name: "reset_edits", Description: "Remove all edit requests and start over.", Parameters: obj(nil)},
	{Name: "export_video", Description: "Open the export dialog to render the final video.", Parameters: obj(nil)},
}

func obj(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }

func (s *Studio) agentSystem(p *editor.Project) string {
	sum := p.Summarize()
	var rules []string
	for _, r := range p.Edits.Rules {
		state := ""
		if !r.Enabled {
			state = "（已停用）"
		}
		rules = append(rules, fmt.Sprintf("- %s：%s%s", map[editor.Mode]string{editor.ModeKeep: "保留", editor.ModeDrop: "去掉", editor.ModeBoost: "加分", editor.ModeMarker: "节点", editor.ModeOpening: "开局", editor.ModeEnding: "结尾"}[r.Mode], r.Text, state))
	}
	for _, r := range p.Edits.Ranges {
		verb := "去掉"
		if r.Keep {
			verb = "保留"
		}
		rules = append(rules, fmt.Sprintf("- %s %s–%s", verb, media.Clock(r.Start), media.Clock(r.End)))
	}
	if len(rules) == 0 {
		rules = []string{"（还没有）"}
	}
	limit := "不限"
	if p.Edits.TargetSeconds > 0 {
		limit = media.Clock(p.Edits.TargetSeconds)
	}
	tpl := p.TemplateName
	if tpl == "" {
		tpl = "无"
	}
	return fmt.Sprintf(`你是「闪剪」里的视频剪辑助手。闪剪用 Cloudflare Clef-omni 逐段看视频，按用户的要求挑出片段，再用 ffmpeg 导出，可以加 Remotion 动效。
需要改剪辑时就调用工具；闲聊或提问就直接简短回答。一句话里有多个要求时，依次调用多个工具。用户说的时间都是原视频的时间。
回答用用户的语言，一两句话说清楚改了什么、现在成片多长，不要编造没有的功能。

当前视频：%s，时长 %s
当前成片：%s，%d 个片段（已分析 %d/%d 段）
模板：%s
时长上限：%s
剪辑要求：
%s`, p.Source.Path[strings.LastIndex(p.Source.Path, "/")+1:], media.Clock(p.Source.Duration), media.Clock(sum.KeptSeconds), sum.Clips, sum.Analyzed, sum.Segments, tpl, limit, strings.Join(rules, "\n"))
}

// agentChat lets the configured chat model answer and edit through tools.
func (s *Studio) agentChat(ctx context.Context, p *editor.Project, message string, events *mygo.Channel[Event]) (*ChatResult, error) {
	st := loadSettings()
	client, provider, err := chatClient(st.Chat)
	if err != nil {
		p.AddMessage("user", message)
		p.AddMessage("assistant", "聊天模型不可用："+err.Error())
		_ = p.Save(projectsDir())
		return &ChatResult{View: s.view(p)}, nil
	}
	msgs := []llm.Message{{Role: "system", Content: s.agentSystem(p)}}
	history := p.Chat
	if len(history) > 12 {
		history = history[len(history)-12:]
	}
	for _, m := range history {
		msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Text})
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: message})
	p.AddMessage("user", message)
	send(events, Event{Kind: "status", Text: "思考中…"})

	result := &ChatResult{}
	var reply string
	for round := 0; round < 5; round++ {
		resp, err := client.Chat(ctx, provider, st.Chat.Model, llm.Request{Messages: msgs, Tools: agentTools, MaxTokens: 800})
		if err != nil {
			reply = "聊天模型出错了：" + err.Error()
			break
		}
		if len(resp.ToolCalls) == 0 {
			reply = resp.Text
			break
		}
		msgs = append(msgs, llm.Message{Role: "assistant", Content: resp.Text, ToolCalls: resp.ToolCalls})
		for _, call := range resp.ToolCalls {
			out := s.runTool(ctx, p, call, events, result)
			msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: call.ID, Content: out})
		}
		if round == 4 {
			reply = "已按你的要求处理完毕。"
		}
	}
	p.Select()
	if strings.TrimSpace(reply) == "" {
		sum := p.Summarize()
		reply = fmt.Sprintf("好的。现在成片 %s，共 %d 个片段。", media.Clock(sum.KeptSeconds), sum.Clips)
	}
	p.AddMessage("assistant", reply)
	if err := p.Save(projectsDir()); err != nil {
		return nil, err
	}
	result.View = s.view(p)
	return result, nil
}

// runTool runs one tool call and returns a short result for the model.
func (s *Studio) runTool(ctx context.Context, p *editor.Project, call llm.ToolCall, events *mygo.Channel[Event], result *ChatResult) string {
	var args struct {
		Description string  `json:"description"`
		Seconds     float64 `json:"seconds"`
		Direction   string  `json:"direction"`
		Start       any     `json:"start"`
		End         any     `json:"end"`
		Name        string  `json:"name"`
	}
	_ = json.Unmarshal(call.Arguments, &args)
	status := func() string {
		p.Select()
		sum := p.Summarize()
		return fmt.Sprintf("现在成片 %s，%d 个片段。", media.Clock(sum.KeptSeconds), sum.Clips)
	}
	analyze := func(what string) string {
		sum := p.Summarize()
		if len(p.Pending()) > 0 {
			send(events, Event{Kind: "status", Text: fmt.Sprintf("%s：逐段分析（约 %d 段，预计 $%.2f）…", what, len(p.Pending()), float64(sum.PendingTokens)/1e6*clef.PricePerMTok[clef.Omni])})
		}
		if err := s.analyze(ctx, p, events); err != nil {
			return "分析没有完成：" + err.Error()
		}
		return ""
	}
	switch call.Name {
	case "keep_content", "remove_content":
		if strings.TrimSpace(args.Description) == "" {
			return "缺少要保留或去掉的内容描述"
		}
		mode := editor.ModeKeep
		if call.Name == "remove_content" {
			mode = editor.ModeDrop
		}
		p.Checkpoint()
		r := p.AddRule(args.Description, mode)
		r.Source = "chat"
		if msg := analyze("“" + args.Description + "”"); msg != "" {
			return msg
		}
		hits := 0
		for _, seg := range p.Segments {
			if v, _, ok := seg.Value(r); ok && v >= r.Threshold {
				hits++
			}
		}
		return fmt.Sprintf("已添加要求“%s”，命中 %d 段。%s", args.Description, hits, status())
	case "smart_cut":
		if msg := analyze("智能剪辑"); msg != "" {
			return msg
		}
		return "已按精彩程度挑选。" + status()
	case "set_length":
		p.Checkpoint()
		p.Edits.TargetSeconds = math.Max(0, math.Min(args.Seconds, p.Source.Duration))
		return "已设置时长上限。" + status()
	case "change_length":
		word := "再短一点"
		if args.Direction == "longer" {
			word = "再长一点"
		}
		// Apply knows how to shorten (a cap) and lengthen (looser matching).
		return p.Apply(word, editor.Intent{Duration: 0.95}).Reply + status()
	case "keep_range", "remove_range":
		start, ok1 := parseTime(args.Start)
		end, ok2 := parseTime(args.End)
		if !ok1 || !ok2 || end <= start {
			return "时间格式不对，需要像 3:20 这样的开始和结束时间"
		}
		p.Checkpoint()
		p.Edits.Ranges = append(p.Edits.Ranges, editor.RangeRule{Start: start, End: math.Min(end, p.Source.Duration), Keep: call.Name == "keep_range"})
		return fmt.Sprintf("已处理 %s–%s。", media.Clock(start), media.Clock(end)) + status()
	case "apply_template":
		var match *preset.Template
		for _, t := range templates().List() {
			if strings.Contains(t.Name, args.Name) || strings.Contains(args.Name, t.Name) || strings.Contains(t.Name, strings.Fields(args.Name+" x")[0]) {
				match = t
				break
			}
		}
		if match == nil {
			var names []string
			for _, t := range templates().List() {
				names = append(names, t.Name)
			}
			return "没有这个模板。可用的有：" + strings.Join(names, "、")
		}
		preset.Apply(p, match)
		if msg := analyze("套用模板「" + match.Name + "」"); msg != "" {
			return msg
		}
		return "已套用模板「" + match.Name + "」。" + status()
	case "undo":
		if !p.Undo() {
			return "没有可以撤销的修改"
		}
		return "已撤销。" + status()
	case "reset_edits":
		p.Checkpoint()
		p.Edits.Rules, p.Edits.Ranges, p.Edits.TargetSeconds = nil, nil, 0
		p.Edits.Overrides = map[int]bool{}
		return "已清空剪辑要求。" + status()
	case "export_video":
		result.Export = true
		return "已打开导出窗口，用户会在窗口里选择动效和保存位置。"
	}
	return "未知的工具：" + call.Name
}

// parseTime reads "3:20", "1:02:03" or seconds.
func parseTime(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		t = strings.TrimSpace(t)
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f, true
		}
		parts := strings.Split(t, ":")
		total := 0.0
		for _, part := range parts {
			n, err := strconv.ParseFloat(part, 64)
			if err != nil {
				return 0, false
			}
			total = total*60 + n
		}
		return total, len(parts) > 1
	}
	return 0, false
}

