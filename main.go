package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"flashcut/internal/clef"
	"flashcut/internal/editor"
	"flashcut/internal/media"
	"flashcut/internal/preset"
)

// Studio is the FlashCut backend: it opens videos as projects, analyzes
// them with Clef-omni, edits them from chat or controls, and exports them.
type Studio struct {
	mu       sync.Mutex
	projects map[string]*entry
	media    map[string]string // media:// id → file path the page may play
}

type entry struct {
	mu sync.Mutex
	p  *editor.Project
}

// View is a project as the page shows it.
type View struct {
	Project  *editor.Project `json:"project"`
	MediaURL string          `json:"mediaUrl"`
	Summary  editor.Summary  `json:"summary"`
	Ranges   []media.Range   `json:"ranges"`
}

// Event reports progress of a long operation.
type Event struct {
	// Kind is "analyze", "export" or "status".
	Kind    string          `json:"kind"`
	Done    int             `json:"done"`
	Total   int             `json:"total"`
	Segment *editor.Segment `json:"segment,omitempty"`
	Text    string          `json:"text,omitempty"`
}

// ChatResult is the outcome of one chat message.
type ChatResult struct {
	View *View `json:"view"`
	// Export is true when the user asked to export; the page then runs it.
	Export bool `json:"export"`
}

// ExportResult is a finished export.
type ExportResult struct {
	Path     string  `json:"path"`
	MediaURL string  `json:"mediaUrl"`
	Duration float64 `json:"duration"`
	Size     int64   `json:"size"`
}

// RecentProject is a saved project in the start screen list.
type RecentProject struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Duration    float64   `json:"duration"`
	KeptSeconds float64   `json:"keptSeconds"`
	Missing     bool      `json:"missing"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Settings are the app preferences as the page sees them. The API token
// itself never leaves Go.
type Settings struct {
	AccountID   string        `json:"accountId"`
	HasToken    bool          `json:"hasToken"`
	Concurrency int           `json:"concurrency"`
	Quality     media.Quality `json:"quality"`
	// Connection says how Clef is reached, or why it cannot be.
	Connection string `json:"connection"`
	Connected  bool   `json:"connected"`
	FFmpeg     string `json:"ffmpeg"`
	// DefaultTemplate is applied to newly opened videos ("" for none).
	DefaultTemplate string `json:"defaultTemplate"`
}

type storedSettings struct {
	clef.Config
	Concurrency     int           `json:"concurrency"`
	Quality         media.Quality `json:"quality"`
	DefaultTemplate string        `json:"defaultTemplate"`
}

// ---- settings ----

func settingsPath() string { return filepath.Join(dataDir(), "settings.json") }

func loadSettings() storedSettings {
	s := storedSettings{Concurrency: 4, Quality: media.QualityMedium}
	if data, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.Concurrency < 1 || s.Concurrency > 8 {
		s.Concurrency = 4
	}
	if s.Quality == "" {
		s.Quality = media.QualityMedium
	}
	return s
}

// GetSettings returns the preferences and checks that ffmpeg and Clef are
// reachable.
func (s *Studio) GetSettings() Settings {
	st := loadSettings()
	out := Settings{AccountID: st.AccountID, HasToken: st.APIToken != "", Concurrency: st.Concurrency, Quality: st.Quality, DefaultTemplate: st.DefaultTemplate}
	if c, err := clef.New(st.Config); err != nil {
		out.Connection = err.Error()
	} else {
		out.Connection, out.Connected = c.Name(), true
	}
	if _, err := media.LookTool("ffmpeg"); err != nil {
		out.FFmpeg = err.Error()
	}
	return out
}

// SaveSettings stores the preferences. An empty apiToken keeps the saved
// one; clear removes the saved credentials so the cf CLI is used.
func (s *Studio) SaveSettings(accountID, apiToken string, clear bool, concurrency int, quality media.Quality, defaultTemplate string) Settings {
	st := loadSettings()
	st.AccountID = strings.TrimSpace(accountID)
	if t := strings.TrimSpace(apiToken); t != "" {
		st.APIToken = t
	}
	if clear {
		st.Config = clef.Config{}
	}
	st.Concurrency, st.Quality, st.DefaultTemplate = concurrency, quality, defaultTemplate
	if data, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.MkdirAll(dataDir(), 0o700)
		_ = os.WriteFile(settingsPath(), data, 0o600)
	}
	return s.GetSettings()
}

func newClient() (*clef.Client, int, error) {
	st := loadSettings()
	c, err := clef.New(st.Config)
	return c, st.Concurrency, err
}

// ---- projects ----

// Recent lists recently edited videos.
func (s *Studio) Recent() []RecentProject {
	var out []RecentProject
	for _, p := range editor.Recent(projectsDir(), 12) {
		_, err := os.Stat(p.Source.Path)
		out = append(out, RecentProject{
			ID: p.ID, Name: filepath.Base(p.Source.Path), Path: p.Source.Path, Duration: p.Source.Duration,
			KeptSeconds: p.Summarize().KeptSeconds, Missing: err != nil, UpdatedAt: p.UpdatedAt,
		})
	}
	return out
}

// PickVideo asks for a video and opens it. It returns null when canceled.
func (s *Studio) PickVideo(ctx context.Context) (*View, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: mygo.CallerWindow(ctx),
		Title:  "选择要剪辑的视频",
		Filters: []mygo.FileFilter{{Name: "视频", Extensions: []string{
			"mp4", "mov", "m4v", "mkv", "webm", "avi", "flv", "ts", "mts", "wmv", "mpg", "mpeg",
		}}},
	})
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	return s.OpenPath(ctx, paths[0])
}

// OpenPath opens a video by path, resuming its project if one exists.
func (s *Studio) OpenPath(ctx context.Context, path string) (*View, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("打不开文件：%w", err)
	}
	info, err := media.Probe(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("无法识别这个视频：%w", err)
	}
	id := editor.ProjectID(info, st.ModTime())
	if v, err := s.Open(id); err == nil {
		return v, nil
	}
	p := editor.New(id, info, editor.DefaultSegmentSeconds)
	p.AddMessage("assistant", fmt.Sprintf("已打开「%s」（%s）。告诉我想怎么剪，比如“只保留精彩片段”“去掉片头广告”“控制在 3 分钟”，或者直接点「智能剪辑」让我挑出最值得看的部分。",
		filepath.Base(path), media.Clock(info.Duration)))
	if id := loadSettings().DefaultTemplate; id != "" {
		if t, err := templates().Get(id); err == nil {
			preset.Apply(p, t)
			p.History = nil
			p.AddMessage("assistant", fmt.Sprintf("已套用默认模板「%s」，点「智能剪辑」开始分析。", t.Name))
		}
	}
	p.Select()
	if err := p.Save(projectsDir()); err != nil {
		return nil, err
	}
	e := &entry{p: p}
	s.mu.Lock()
	s.projects[id] = e
	s.mu.Unlock()
	return s.view(p), nil
}

// Open opens a saved project.
func (s *Studio) Open(id string) (*View, error) {
	e, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := os.Stat(e.p.Source.Path); err != nil {
		return nil, fmt.Errorf("原视频不见了：%s", e.p.Source.Path)
	}
	return s.view(e.p), nil
}

// Initial opens the video named by FLASHCUT_OPEN, if set, so the app can be
// started on a file from the command line. It returns null otherwise.
func (s *Studio) Initial(ctx context.Context) (*View, error) {
	path := os.Getenv("FLASHCUT_OPEN")
	if path == "" {
		return nil, nil
	}
	return s.OpenPath(ctx, path)
}

// Forget removes a project from the recent list. The video is untouched.
func (s *Studio) Forget(id string) error {
	if strings.ContainsAny(id, `/\.`) || id == "" {
		return errors.New("invalid project id")
	}
	s.mu.Lock()
	delete(s.projects, id)
	s.mu.Unlock()
	return os.RemoveAll(filepath.Join(projectsDir(), id))
}

func (s *Studio) entry(id string) (*entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.projects[id]; ok {
		return e, nil
	}
	if strings.ContainsAny(id, `/\.`) || id == "" {
		return nil, errors.New("invalid project id")
	}
	p, err := editor.Load(projectsDir(), id)
	if err != nil {
		return nil, fmt.Errorf("找不到这个项目：%w", err)
	}
	e := &entry{p: p}
	s.projects[id] = e
	return e, nil
}

// edit runs fn on a project that is not busy, then reselects and saves.
func (s *Studio) edit(id string, fn func(p *editor.Project) error) (*View, error) {
	e, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	if !e.mu.TryLock() {
		return nil, errors.New("正在处理上一个操作，请稍候")
	}
	defer e.mu.Unlock()
	if err := fn(e.p); err != nil {
		return nil, err
	}
	e.p.Select()
	if err := e.p.Save(projectsDir()); err != nil {
		return nil, err
	}
	return s.view(e.p), nil
}

// ---- editing ----

// SetOptions changes the automatic selection: the interest level used when
// there are no keep rules, the length cap (0 for none), gap filling and the
// seconds added around each clip.
func (s *Studio) SetOptions(id string, interestThreshold, targetSeconds float64, fillGaps bool, padSeconds float64) (*View, error) {
	return s.edit(id, func(p *editor.Project) error {
		p.Checkpoint()
		p.Edits.InterestThreshold, p.Edits.TargetSeconds, p.Edits.FillGaps = interestThreshold, max(0, targetSeconds), fillGaps
		p.Edits.PadSeconds = min(max(padSeconds, 0), 10)
		return nil
	})
}

// SetRule enables, disables or retunes a content rule.
func (s *Studio) SetRule(id, ruleID string, enabled bool, threshold float64) (*View, error) {
	return s.edit(id, func(p *editor.Project) error {
		r := p.Rule(ruleID)
		if r == nil {
			return errors.New("这条要求已经不存在")
		}
		p.Checkpoint()
		r = p.Rule(ruleID)
		hi := 0.99
		if r.Question != nil && r.Question.Type == "score" {
			hi = float64(max(r.Question.Levels()-1, 1))
		}
		r.Enabled, r.Threshold = enabled, min(max(threshold, 0), hi)
		return nil
	})
}

// DeleteRule removes a content rule.
func (s *Studio) DeleteRule(id, ruleID string) (*View, error) {
	return s.edit(id, func(p *editor.Project) error {
		p.Checkpoint()
		for i, r := range p.Edits.Rules {
			if r.ID == ruleID {
				p.Edits.Rules = append(p.Edits.Rules[:i], p.Edits.Rules[i+1:]...)
				break
			}
		}
		return nil
	})
}

// DeleteRange removes a time-range rule by position.
func (s *Studio) DeleteRange(id string, index int) (*View, error) {
	return s.edit(id, func(p *editor.Project) error {
		if index < 0 || index >= len(p.Edits.Ranges) {
			return errors.New("这个时间段已经不存在")
		}
		p.Checkpoint()
		p.Edits.Ranges = append(p.Edits.Ranges[:index], p.Edits.Ranges[index+1:]...)
		return nil
	})
}

// SetOverride keeps ("keep"), drops ("drop") or un-pins ("auto") a segment.
func (s *Studio) SetOverride(id string, index int, mode string) (*View, error) {
	return s.edit(id, func(p *editor.Project) error {
		p.Checkpoint()
		switch mode {
		case "keep":
			p.Edits.Overrides[index] = true
		case "drop":
			p.Edits.Overrides[index] = false
		default:
			delete(p.Edits.Overrides, index)
		}
		return nil
	})
}

// Undo reverts the last edit.
func (s *Studio) Undo(id string) (*View, error) {
	return s.edit(id, func(p *editor.Project) error {
		if !p.Undo() {
			return errors.New("没有可以撤销的修改了")
		}
		return nil
	})
}

// ---- model work ----

// Analyze asks the model about every segment that has unanswered
// questions, streaming progress, then reselects.
func (s *Studio) Analyze(ctx context.Context, id string, events *mygo.Channel[Event]) (*View, error) {
	e, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	if !e.mu.TryLock() {
		return nil, errors.New("正在处理上一个操作，请稍候")
	}
	defer e.mu.Unlock()
	if err := s.analyze(ctx, e.p, events); err != nil {
		return nil, err
	}
	return s.view(e.p), nil
}

func (s *Studio) analyze(ctx context.Context, p *editor.Project, events *mygo.Channel[Event]) error {
	if len(p.Pending()) == 0 {
		p.Select()
		return p.Save(projectsDir())
	}
	client, workers, err := newClient()
	if err != nil {
		return err
	}
	scratch := filepath.Join(cacheDir(), p.ID)
	defer os.RemoveAll(scratch)
	err = p.Analyze(ctx, client, scratch, workers, func(pr editor.Progress) {
		send(events, Event{Kind: "analyze", Done: pr.Done, Total: pr.Total, Segment: pr.Segment})
	}, func() { _ = p.Save(projectsDir()) })
	p.Select()
	if serr := p.Save(projectsDir()); err == nil {
		err = serr
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("已停止分析，已完成的部分会保留")
	}
	return err
}

// Chat applies a chat message: Clef-flash works out what the user wants,
// the edits change, and new content rules are analyzed.
func (s *Studio) Chat(ctx context.Context, id, message string, events *mygo.Channel[Event]) (*ChatResult, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, errors.New("消息是空的")
	}
	e, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	if !e.mu.TryLock() {
		return nil, errors.New("正在处理上一个操作，请稍候")
	}
	defer e.mu.Unlock()
	p := e.p
	client, _, err := newClient()
	if err != nil {
		return nil, err
	}
	p.AddMessage("user", message)
	send(events, Event{Kind: "status", Text: "理解中…"})
	intent, tokens, err := editor.Understand(ctx, client, message)
	if err != nil {
		p.AddMessage("assistant", "没能理解这句话："+err.Error())
		_ = p.Save(projectsDir())
		return &ChatResult{View: s.view(p)}, nil
	}
	_ = tokens // Clef-flash costs a fraction of a cent per message
	out := p.Apply(message, intent)
	reply := out.Reply
	if out.Analyze || (out.Changed && len(p.Pending()) > 0) {
		sum := p.Summarize()
		send(events, Event{Kind: "status", Text: fmt.Sprintf("%s\n正在逐段分析（约 %d 段，预计 $%.2f）…", reply, len(p.Pending()), float64(sum.PendingTokens)/1e6*clef.PricePerMTok[clef.Omni])})
		if err := s.analyze(ctx, p, events); err != nil {
			p.AddMessage("assistant", reply+"\n分析没有完成："+err.Error())
			_ = p.Save(projectsDir())
			return &ChatResult{View: s.view(p)}, nil
		}
	}
	p.Select()
	if out.Changed || out.Analyze {
		sum := p.Summarize()
		reply += fmt.Sprintf("\n现在成片 %s，共 %d 个片段。", media.Clock(sum.KeptSeconds), sum.Clips)
		if hits := ruleHits(p); hits != "" {
			reply += hits
		}
	}
	p.AddMessage("assistant", reply)
	if err := p.Save(projectsDir()); err != nil {
		return nil, err
	}
	return &ChatResult{View: s.view(p), Export: out.Export}, nil
}

// ruleHits describes how many segments the newest rule matched.
func ruleHits(p *editor.Project) string {
	if len(p.Edits.Rules) == 0 {
		return ""
	}
	r := p.Edits.Rules[len(p.Edits.Rules)-1]
	n := 0
	for _, s := range p.Segments {
		if v, ok := s.Answers[r.ID]; ok && v >= r.Threshold {
			n++
		}
	}
	if time.Since(r.CreatedAt) > time.Minute {
		return ""
	}
	return fmt.Sprintf("“%s”命中 %d 段。", r.Text, n)
}

// ---- templates ----

func templates() preset.Store { return preset.Store{Dir: filepath.Join(dataDir(), "templates")} }

// Templates lists the built-in and saved templates.
func (s *Studio) Templates() []*preset.Template { return templates().List() }

// SaveTemplate validates and stores t. Saving a built-in stores a copy.
func (s *Studio) SaveTemplate(t preset.Template) (*preset.Template, error) {
	return templates().Save(&t)
}

// DeleteTemplate removes a saved template.
func (s *Studio) DeleteTemplate(id string) error { return templates().Delete(id) }

// ApplyTemplate sets a project's template rules and settings, then
// analyzes what the new questions need. Earlier answers are reused.
func (s *Studio) ApplyTemplate(ctx context.Context, id, templateID string, events *mygo.Channel[Event]) (*View, error) {
	t, err := templates().Get(templateID)
	if err != nil {
		return nil, err
	}
	e, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	if !e.mu.TryLock() {
		return nil, errors.New("正在处理上一个操作，请稍候")
	}
	defer e.mu.Unlock()
	preset.Apply(e.p, t)
	sum := e.p.Summarize()
	e.p.AddMessage("assistant", fmt.Sprintf("已套用模板「%s」（%d 个问题）。", t.Name, len(t.Questions)))
	send(events, Event{Kind: "status", Text: fmt.Sprintf("套用模板「%s」，逐段分析（约 %d 段，预计 $%.2f）…", t.Name, len(e.p.Pending()), float64(sum.PendingTokens)/1e6*clef.PricePerMTok[clef.Omni])})
	if err := s.analyze(ctx, e.p, events); err != nil {
		return s.view(e.p), err
	}
	sum = e.p.Summarize()
	e.p.AddMessage("assistant", fmt.Sprintf("分析完成：成片 %s，共 %d 个片段。", media.Clock(sum.KeptSeconds), sum.Clips))
	_ = e.p.Save(projectsDir())
	return s.view(e.p), nil
}

// SaveProjectAsTemplate turns a project's requests into a new template.
func (s *Studio) SaveProjectAsTemplate(id, name string) (*preset.Template, error) {
	e, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	t := preset.FromProject(e.p, strings.TrimSpace(name))
	e.mu.Unlock()
	if len(t.Questions) == 0 {
		return nil, errors.New("这个项目还没有剪辑要求，先在聊天里说几句再存成模板")
	}
	return templates().Save(t)
}

// ImportTemplate reads a template file the user picks.
func (s *Studio) ImportTemplate(ctx context.Context) (*preset.Template, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:  mygo.CallerWindow(ctx),
		Title:   "导入模板",
		Filters: []mygo.FileFilter{{Name: "闪剪模板", Extensions: []string{"json"}}},
	})
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return nil, err
	}
	t, err := preset.Parse(data)
	if err != nil {
		return nil, err
	}
	t.ID = "" // always a new saved copy
	return templates().Save(t)
}

// ExportTemplate writes a template to a file the user picks. It returns
// the path, or "" when canceled.
func (s *Studio) ExportTemplate(ctx context.Context, templateID string) (string, error) {
	t, err := templates().Get(templateID)
	if err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	out, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:      mygo.CallerWindow(ctx),
		Title:       "导出模板",
		DefaultPath: filepath.Join(home, "Downloads", t.Name+".flashcut.json"),
		Filters:     []mygo.FileFilter{{Name: "闪剪模板", Extensions: []string{"json"}}},
	})
	if err != nil || out == "" {
		return "", err
	}
	data, err := preset.Export(t)
	if err != nil {
		return "", err
	}
	return out, os.WriteFile(out, data, 0o644)
}

// ---- export ----

// Export asks where to save, then cuts and joins the selection.
func (s *Studio) Export(ctx context.Context, id string, events *mygo.Channel[Event]) (*ExportResult, error) {
	e, err := s.entry(id)
	if err != nil {
		return nil, err
	}
	if !e.mu.TryLock() {
		return nil, errors.New("正在处理上一个操作，请稍候")
	}
	defer e.mu.Unlock()
	p := e.p
	ranges := p.Ranges()
	if len(ranges) == 0 {
		return nil, errors.New("还没有要保留的片段")
	}
	home, _ := os.UserHomeDir()
	outDir := filepath.Join(home, "Movies", "闪剪")
	_ = os.MkdirAll(outDir, 0o755)
	base := strings.TrimSuffix(filepath.Base(p.Source.Path), filepath.Ext(p.Source.Path))
	out, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:            mygo.CallerWindow(ctx),
		Title:             "导出成片",
		DefaultPath:       filepath.Join(outDir, base+"-闪剪.mp4"),
		Filters:           []mygo.FileFilter{{Name: "MP4 视频", Extensions: []string{"mp4"}}},
		CreateDirectories: true,
	})
	if err != nil || out == "" {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(out), ".mp4") {
		out += ".mp4"
	}
	if filepath.Clean(out) == filepath.Clean(p.Source.Path) {
		return nil, errors.New("不能覆盖原视频，请换一个文件名")
	}
	return s.export(ctx, p, out, events)
}

func (s *Studio) export(ctx context.Context, p *editor.Project, out string, events *mygo.Channel[Event]) (*ExportResult, error) {
	ranges := p.Ranges()
	err := media.Render(ctx, p.Source.Path, out, ranges, loadSettings().Quality, func(done, total int) {
		send(events, Event{Kind: "export", Done: done, Total: total})
	})
	if errors.Is(err, context.Canceled) {
		return nil, errors.New("已取消导出")
	}
	if err != nil {
		return nil, err
	}
	info, err := media.Probe(ctx, out)
	if err != nil {
		return nil, err
	}
	p.AddMessage("assistant", fmt.Sprintf("已导出 %s（%s，%.0f MB）。", filepath.Base(out), media.Clock(info.Duration), float64(info.Size)/1e6))
	_ = p.Save(projectsDir())
	return &ExportResult{Path: out, MediaURL: s.share(out) + fmt.Sprintf("?v=%d", time.Now().UnixNano()), Duration: info.Duration, Size: info.Size}, nil
}

// Reveal shows a file in Finder.
func (s *Studio) Reveal(path string) { mygo.Shell.ShowItemInFolder(path) }

// ---- helpers ----

func (s *Studio) view(p *editor.Project) *View {
	return &View{Project: p, MediaURL: s.share(p.Source.Path), Summary: p.Summarize(), Ranges: p.Ranges()}
}

// share lets the page play path at a media:// URL. Only files the user
// opened or the app exported are served.
func (s *Studio) share(path string) string {
	sum := sha1.Sum([]byte(path))
	id := hex.EncodeToString(sum[:8]) + strings.ToLower(filepath.Ext(path))
	s.mu.Lock()
	s.media[id] = path
	s.mu.Unlock()
	return "media://localhost/" + id
}

func (s *Studio) serveMedia(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	path, ok := s.media[strings.TrimPrefix(r.URL.Path, "/")]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func dataDir() string {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		dir = filepath.Join(os.TempDir(), "flashcut")
	}
	return dir
}

func projectsDir() string { return filepath.Join(dataDir(), "projects") }

func cacheDir() string {
	dir, err := mygo.App.Path(mygo.PathCache)
	if err != nil {
		dir = filepath.Join(os.TempDir(), "flashcut-cache")
	}
	return dir
}

func main() {
	studio := &Studio{projects: map[string]*entry{}, media: map[string]string{}}
	mygo.Bind(studio)
	if err := mygo.Protocol.HandleFunc("media", studio.serveMedia); err != nil {
		log.Fatal(err)
	}

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "闪剪",
			Width:           1360,
			Height:          880,
			MinWidth:        960,
			MinHeight:       620,
			BackgroundColor: "light-dark(#f6f7f9, #0e1014)", // the page's --bg in src/style.css
			StateKey:        "main",
			URL:             "/",
		})
		if os.Getenv("FLASHCUT_SELFTEST") != "" {
			go selfTest(win, studio)
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// send reports progress when the caller is listening.
func send(ch *mygo.Channel[Event], e Event) {
	if ch != nil {
		_ = ch.Send(e)
	}
}
