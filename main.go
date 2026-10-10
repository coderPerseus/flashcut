package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
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

	"lol-highlights/internal/highlight"
)

// Studio turns a full match video into a highlight reel: it splits the video
// into segments, asks Clef-omni whether each is worth keeping, and joins the
// kept ones with ffmpeg.
type Studio struct {
	mu    sync.Mutex
	media map[string]string // media:// id → file path the page may play
}

// Project is the state of one source video as the page shows it.
type Project struct {
	Source   highlight.VideoInfo `json:"source"`
	MediaURL string              `json:"mediaUrl"`
	Segments []highlight.Segment `json:"segments"`
	Ranges   []highlight.Range   `json:"ranges"`
	Stats    highlight.Stats     `json:"stats"`
	Options  highlight.Options   `json:"options"`
	Judge    string              `json:"judge"`
}

// RenderResult is the finished highlight reel.
type RenderResult struct {
	Path     string  `json:"path"`
	MediaURL string  `json:"mediaUrl"`
	Duration float64 `json:"duration"`
	Size     int64   `json:"size"`
}

// RenderProgress reports cut parts as they finish.
type RenderProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Defaults returns the default analysis options.
func (s *Studio) Defaults() highlight.Options { return highlight.DefaultOptions() }

// PickVideo asks for a match video and returns it, with any earlier
// analysis of it. It returns null when the dialog is canceled.
func (s *Studio) PickVideo(ctx context.Context) (*Project, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:  mygo.CallerWindow(ctx),
		Title:   "选择比赛视频",
		Filters: []mygo.FileFilter{{Name: "视频", Extensions: []string{"mp4", "mov", "mkv", "flv", "webm", "ts"}}},
	})
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	return s.Open(ctx, paths[0])
}

// Initial opens the video named by LOL_HIGHLIGHTS_OPEN, if set, so the app
// can be started on a file from the command line. It returns null otherwise.
func (s *Studio) Initial(ctx context.Context) (*Project, error) {
	path := os.Getenv("LOL_HIGHLIGHTS_OPEN")
	if path == "" {
		return nil, nil
	}
	return s.Open(ctx, path)
}

// Open loads a video by path, with any earlier analysis of it.
func (s *Studio) Open(ctx context.Context, path string) (*Project, error) {
	info, err := highlight.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	p := &Project{Source: info, MediaURL: s.share(path), Options: highlight.DefaultOptions()}
	if a, err := highlight.LoadAnalysis(workDir(path)); err == nil {
		p.fill(a)
		p.Ranges = a.Ranges()
		p.Options.SegmentSeconds = a.SegmentSeconds
	}
	return p, nil
}

// Analyze judges every segment of the video, streaming each verdict to
// progress, then selects segments with opts. Segments judged in an earlier
// run with the same segment length are reused.
func (s *Studio) Analyze(ctx context.Context, path string, opts highlight.Options, progress *mygo.Channel[highlight.Progress]) (*Project, error) {
	judge, err := highlight.NewJudge()
	if err != nil {
		return nil, err
	}
	wd := workDir(path)
	a, err := highlight.Analyze(ctx, judge, path, wd, opts, func(p highlight.Progress) {
		_ = progress.Send(p)
	})
	if err != nil {
		return nil, err
	}
	a.Select(opts)
	if err := a.Save(wd); err != nil {
		return nil, err
	}
	return s.project(path, a, opts), nil
}

// Reselect applies new thresholds to the saved analysis without calling the
// model again. Manual changes are replaced.
func (s *Studio) Reselect(path string, opts highlight.Options) (*Project, error) {
	wd := workDir(path)
	a, err := highlight.LoadAnalysis(wd)
	if err != nil {
		return nil, errors.New("这个视频还没有分析过")
	}
	a.Select(opts)
	if err := a.Save(wd); err != nil {
		return nil, err
	}
	return s.project(path, a, opts), nil
}

// SetKept keeps or drops one segment by hand.
func (s *Studio) SetKept(path string, index int, kept bool, opts highlight.Options) (*Project, error) {
	wd := workDir(path)
	a, err := highlight.LoadAnalysis(wd)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(a.Segments) {
		return nil, fmt.Errorf("no segment %d", index)
	}
	a.Segments[index].Kept = kept
	if err := a.Save(wd); err != nil {
		return nil, err
	}
	return s.project(path, a, opts), nil
}

// Render cuts the kept segments from the source and joins them into
// "<name>-highlights.mp4" in ~/Movies/LoL Highlights.
func (s *Studio) Render(ctx context.Context, path string, progress *mygo.Channel[RenderProgress]) (*RenderResult, error) {
	a, err := highlight.LoadAnalysis(workDir(path))
	if err != nil {
		return nil, errors.New("请先分析视频")
	}
	home, _ := os.UserHomeDir()
	outDir := filepath.Join(home, "Movies", "LoL Highlights")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	out := filepath.Join(outDir, base+"-highlights.mp4")
	err = highlight.Render(ctx, path, out, a.Ranges(), func(done, total int) {
		_ = progress.Send(RenderProgress{Done: done, Total: total})
	})
	if err != nil {
		return nil, err
	}
	info, err := highlight.Probe(ctx, out)
	if err != nil {
		return nil, err
	}
	return &RenderResult{Path: out, MediaURL: s.share(out) + fmt.Sprintf("?v=%d", time.Now().Unix()), Duration: info.Duration, Size: info.Size}, nil
}

// Reveal shows a file in Finder.
func (s *Studio) Reveal(path string) { mygo.Shell.ShowItemInFolder(path) }

func (s *Studio) project(path string, a *highlight.Analysis, opts highlight.Options) *Project {
	p := &Project{Source: a.Source, MediaURL: s.share(path), Options: opts}
	p.Options.SegmentSeconds = a.SegmentSeconds
	p.fill(a)
	p.Ranges = a.Ranges()
	return p
}

func (p *Project) fill(a *highlight.Analysis) {
	p.Segments = a.Segments
	p.Stats = a.Stats()
	p.Judge = a.Judge
}

// share lets the page play path at a media:// URL. Only files the user
// picked or the app rendered are served.
func (s *Studio) share(path string) string {
	sum := sha1.Sum([]byte(path))
	id := hex.EncodeToString(sum[:8]) + filepath.Ext(path)
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

func workDir(path string) string {
	cache, err := mygo.App.Path(mygo.PathCache)
	if err != nil {
		cache = filepath.Join(os.TempDir(), "lol-highlights")
	}
	return highlight.WorkDir(cache, path)
}

func main() {
	studio := &Studio{media: map[string]string{}}
	mygo.Bind(studio)
	if err := mygo.Protocol.HandleFunc("media", studio.serveMedia); err != nil {
		log.Fatal(err)
	}

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "LoL 高光剪辑",
			Width:           1180,
			Height:          820,
			MinWidth:        760,
			MinHeight:       560,
			BackgroundColor: "light-dark(#f6f7f9, #0e1014)", // the page's --bg in src/style.css
			StateKey:        "main",
			URL:             "/",
		})
		if os.Getenv("LOL_HIGHLIGHTS_SELFTEST") != "" {
			go selfTest(win)
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
