package highlight

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// extraPaths are searched when a tool is missing from PATH, which is common
// for apps launched from Finder.
var extraPaths = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"}

// LookTool finds an executable on PATH or in common install locations.
func LookTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range extraPaths {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found; install it (e.g. brew install ffmpeg)", name)
}

// VideoInfo describes a source video.
type VideoInfo struct {
	Path     string  `json:"path"`
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Size     int64   `json:"size"`
	HasAudio bool    `json:"hasAudio"`
}

// Probe reads duration, resolution and audio presence with ffprobe.
func Probe(ctx context.Context, path string) (VideoInfo, error) {
	ffprobe, err := LookTool("ffprobe")
	if err != nil {
		return VideoInfo{}, err
	}
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error",
		"-show_entries", "format=duration:stream=codec_type,width,height",
		"-of", "json", path).Output()
	if err != nil {
		return VideoInfo{}, fmt.Errorf("ffprobe %s: %w", filepath.Base(path), err)
	}
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return VideoInfo{}, err
	}
	info := VideoInfo{Path: path}
	info.Duration, _ = strconv.ParseFloat(probe.Format.Duration, 64)
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video":
			if info.Width == 0 {
				info.Width, info.Height = s.Width, s.Height
			}
		case "audio":
			info.HasAudio = true
		}
	}
	if st, err := os.Stat(path); err == nil {
		info.Size = st.Size()
	}
	if info.Duration <= 0 {
		return info, fmt.Errorf("could not read the duration of %s", filepath.Base(path))
	}
	return info, nil
}

// ExtractPreview writes a small, low-frame-rate clip of [start, start+dur)
// for the model: 480p, 4 fps, mono audio. The model samples 2 fps, so this
// keeps each request a few hundred KB instead of many MB.
func ExtractPreview(ctx context.Context, src, dst string, start, dur float64) error {
	ffmpeg, err := LookTool("ffmpeg")
	if err != nil {
		return err
	}
	return run(ctx, ffmpeg, "-v", "error", "-y",
		"-ss", ftoa(start), "-t", ftoa(dur), "-i", src,
		"-vf", "scale=-2:480,fps=4",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "30",
		"-c:a", "aac", "-ac", "1", "-b:a", "48k",
		"-movflags", "+faststart", dst)
}

// Range is a span of the source video in seconds.
type Range struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Render cuts each range from src at full quality and joins them into dst.
// Each part is re-encoded so cuts land exactly on the range boundaries.
// progress, when set, is called after each part with (done, total).
func Render(ctx context.Context, src, dst string, ranges []Range, progress func(done, total int)) error {
	if len(ranges) == 0 {
		return fmt.Errorf("nothing to render: no segments were kept")
	}
	ffmpeg, err := LookTool("ffmpeg")
	if err != nil {
		return err
	}
	// The concat list resolves relative paths against itself, so use absolute ones.
	if dst, err = filepath.Abs(dst); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".parts-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	var list strings.Builder
	for i, r := range ranges {
		part := filepath.Join(tmp, fmt.Sprintf("part-%04d.mp4", i))
		if err := run(ctx, ffmpeg, "-v", "error", "-y",
			"-ss", ftoa(r.Start), "-t", ftoa(r.End-r.Start), "-i", src,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
			"-pix_fmt", "yuv420p", "-r", "30",
			"-c:a", "aac", "-b:a", "160k", "-ar", "48000", "-ac", "2",
			part); err != nil {
			return fmt.Errorf("cut %s-%s: %w", Clock(r.Start), Clock(r.End), err)
		}
		fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(part, "'", `'\''`))
		if progress != nil {
			progress(i+1, len(ranges))
		}
	}
	listPath := filepath.Join(tmp, "list.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		return err
	}
	return run(ctx, ffmpeg, "-v", "error", "-y", "-f", "concat", "-safe", "0",
		"-i", listPath, "-c", "copy", "-movflags", "+faststart", dst)
}

func run(ctx context.Context, name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return fmt.Errorf("%s: %w: %s", filepath.Base(name), err, msg)
	}
	return nil
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

// Clock formats seconds as m:ss or h:mm:ss.
func Clock(sec float64) string {
	s := int(sec + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
