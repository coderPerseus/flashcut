package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"

	"flashcut/internal/llm"
	"flashcut/internal/media"
	"flashcut/internal/subs"
)

// SubJobView is a subtitle check as the page sees it.
type SubJobView struct {
	Job      *subs.Job           `json:"job"`
	MediaURL string              `json:"mediaUrl"`
	Counts   map[subs.Status]int `json:"counts"`
	CostUSD  float64             `json:"costUsd"`
}

// SubProgress reports a checked cue.
type SubProgress struct {
	Done  int        `json:"done"`
	Total int        `json:"total"`
	Item  *subs.Item `json:"item,omitempty"`
	Stage string     `json:"stage,omitempty"`
}

var subsMu sync.Mutex // one check at a time

func subsDir() string { return filepath.Join(dataDir(), "subtitles") }

func (s *Studio) subView(j *subs.Job) *SubJobView {
	return &SubJobView{Job: j, MediaURL: s.share(j.Video.Path), Counts: j.Summary(), CostUSD: float64(j.Tokens) / 1e6 * 0.15}
}

// SubsRecent lists earlier subtitle checks.
func (s *Studio) SubsRecent() []*SubJobView {
	var out []*SubJobView
	for _, j := range subs.Recent(subsDir(), 10) {
		j.Items = nil // the list only needs the header
		out = append(out, &SubJobView{Job: j})
	}
	return out
}

// SubsPick asks for a video ("video") or a subtitle file ("subtitle") and
// returns its path, or "" when canceled.
func (s *Studio) SubsPick(ctx context.Context, kind string) (string, error) {
	opts := mygo.OpenDialogOptions{Parent: mygo.CallerWindow(ctx), Title: "选择视频",
		Filters: []mygo.FileFilter{{Name: "视频", Extensions: []string{"mp4", "mov", "m4v", "mkv", "webm", "avi", "flv", "ts"}}}}
	if kind == "subtitle" {
		opts.Title = "选择字幕文件"
		opts.Filters = []mygo.FileFilter{{Name: "字幕", Extensions: []string{"srt", "vtt", "ass", "ssa"}}}
	}
	paths, err := mygo.Dialog.Open(opts)
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// SubsCreate prepares a check of subtitle against video. source is "auto",
// "audio" or "burned". An earlier check of the same files is resumed.
func (s *Studio) SubsCreate(ctx context.Context, video, subtitle string, source subs.Source) (*SubJobView, error) {
	info, err := media.Probe(ctx, video)
	if err != nil {
		return nil, fmt.Errorf("无法识别这个视频：%w", err)
	}
	data, err := os.ReadFile(subtitle)
	if err != nil {
		return nil, err
	}
	j, err := subs.NewJob(info, subtitle, data, source)
	if err != nil {
		return nil, err
	}
	if old, err := subs.Load(subsDir(), j.ID); err == nil && old.Setting == source {
		return s.subView(old), nil
	}
	if err := j.Save(subsDir()); err != nil {
		return nil, err
	}
	return s.subView(j), nil
}

// SubsOpen opens a saved check.
func (s *Studio) SubsOpen(id string) (*SubJobView, error) {
	j, err := subs.Load(subsDir(), id)
	if err != nil {
		return nil, err
	}
	return s.subView(j), nil
}

// SubsRun checks every unchecked cue, streaming results.
func (s *Studio) SubsRun(ctx context.Context, id string, progress *mygo.Channel[SubProgress]) (*SubJobView, error) {
	if !subsMu.TryLock() {
		return nil, errors.New("已经有一个字幕校对在进行")
	}
	defer subsMu.Unlock()
	j, err := subs.Load(subsDir(), id)
	if err != nil {
		return nil, err
	}
	st := loadSettings()
	cf, workers, err := newClient()
	if err != nil {
		return nil, err
	}
	fixProvider, ok := st.provider(st.Fixer.Provider)
	if !ok {
		return nil, errors.New("字幕纠错模型的服务不存在，请在模型设置里重新选择")
	}
	visionProvider, ok := st.provider(st.Vision.Provider)
	if !ok {
		return nil, errors.New("读画面字幕的模型的服务不存在，请在模型设置里重新选择")
	}
	c := &subs.Checker{
		Clef: cf, LLM: llm.New(cf), Workers: max(workers, 6), Scratch: filepath.Join(cacheDir(), "subs-"+j.ID),
		Fixer:  subs.Model{Provider: fixProvider, Name: st.Fixer.Model},
		Vision: subs.Model{Provider: visionProvider, Name: st.Vision.Model},
	}
	defer os.RemoveAll(c.Scratch)
	err = c.Run(ctx, j, func(p subs.Progress) {
		if progress != nil {
			_ = progress.Send(SubProgress{Done: p.Done, Total: p.Total, Item: p.Item, Stage: p.Stage})
		}
	}, func() { _ = j.Save(subsDir()) })
	_ = j.Save(subsDir())
	if errors.Is(err, context.Canceled) {
		return s.subView(j), nil
	}
	if err != nil {
		return nil, err
	}
	return s.subView(j), nil
}

// SubsEdit sets whether a cue's suggestion is used, and optionally its text.
func (s *Studio) SubsEdit(id string, index int, accepted bool, text string) (*SubJobView, error) {
	j, err := subs.Load(subsDir(), id)
	if err != nil {
		return nil, err
	}
	for i := range j.Items {
		if j.Items[i].Index == index {
			it := &j.Items[i]
			if t := strings.TrimSpace(text); t != "" && t != it.Plain() {
				it.Suggestion = t
				if it.Status == subs.StatusOK || it.Status == subs.StatusSuspect {
					it.Status = subs.StatusFixed
					it.Reason = "手动修改"
				}
			}
			it.Accepted = accepted && it.Suggestion != ""
		}
	}
	if err := j.Save(subsDir()); err != nil {
		return nil, err
	}
	return s.subView(j), nil
}

// SubsRecheck clears results so every cue is checked again.
func (s *Studio) SubsRecheck(id string) (*SubJobView, error) {
	j, err := subs.Load(subsDir(), id)
	if err != nil {
		return nil, err
	}
	for i := range j.Items {
		j.Items[i] = subs.Item{Cue: j.Items[i].Cue, Status: subs.StatusPending}
	}
	j.Source = ""
	if err := j.Save(subsDir()); err != nil {
		return nil, err
	}
	return s.subView(j), nil
}

// SubsExport writes the corrected subtitles where the user picks. It
// returns the path, or "" when canceled.
func (s *Studio) SubsExport(ctx context.Context, id string) (string, error) {
	j, err := subs.Load(subsDir(), id)
	if err != nil {
		return "", err
	}
	data, err := j.Export()
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(j.SubPath)
	base := strings.TrimSuffix(filepath.Base(j.SubPath), ext)
	out, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent: mygo.CallerWindow(ctx), Title: "导出修正后的字幕",
		DefaultPath: filepath.Join(filepath.Dir(j.SubPath), base+".修正"+ext),
	})
	if err != nil || out == "" {
		return "", err
	}
	if filepath.Clean(out) == filepath.Clean(j.SubPath) {
		return "", errors.New("不能覆盖原字幕文件，请换一个文件名")
	}
	return out, os.WriteFile(out, data, 0o644)
}
