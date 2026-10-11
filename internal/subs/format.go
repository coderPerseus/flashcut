// Package subs checks a subtitle file against its video and fixes the
// lines that do not match what is said (or what is burned into the frame).
package subs

import (
	"bufio"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Cue is one subtitle.
type Cue struct {
	Index int     `json:"index"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	// raw keeps an ASS line's fields so it can be written back unchanged.
	raw string
}

// Format is a subtitle file format.
type Format string

const (
	SRT Format = "srt"
	VTT Format = "vtt"
	ASS Format = "ass"
)

// File is a parsed subtitle file.
type File struct {
	Format Format `json:"format"`
	Cues   []Cue  `json:"cues"`
	// header holds what comes before the cues (VTT header, ASS sections).
	header string
}

var (
	reTiming = regexp.MustCompile(`^\s*(\d+:)?(\d{1,2}):(\d{2})[,.](\d{1,3})\s*-->\s*(\d+:)?(\d{1,2}):(\d{2})[,.](\d{1,3})`)
	reTag    = regexp.MustCompile(`<[^>]+>|\{\\[^}]*\}`)
)

// Parse reads SRT, WebVTT or ASS/SSA. name is used to pick the format
// when the content is ambiguous.
func Parse(name string, data []byte) (*File, error) {
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\uFEFF"), "\r\n", "\n")
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(strings.TrimSpace(text), "WEBVTT") || strings.HasSuffix(lower, ".vtt"):
		return parseCues(text, VTT)
	case strings.Contains(text, "[Events]") || strings.HasSuffix(lower, ".ass") || strings.HasSuffix(lower, ".ssa"):
		return parseASS(text)
	default:
		return parseCues(text, SRT)
	}
}

func parseCues(text string, f Format) (*File, error) {
	out := &File{Format: f}
	lines := strings.Split(text, "\n")
	if f == VTT {
		// Keep the header block (WEBVTT line, metadata) verbatim.
		end := 0
		for end < len(lines) && strings.TrimSpace(lines[end]) != "" {
			end++
		}
		out.header = strings.Join(lines[:end], "\n")
		lines = lines[end:]
	}
	for i := 0; i < len(lines); i++ {
		m := reTiming.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		c := Cue{Index: len(out.Cues) + 1, Start: secs(m[1], m[2], m[3], m[4]), End: secs(m[5], m[6], m[7], m[8])}
		var body []string
		for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
			i++
			body = append(body, lines[i])
		}
		c.Text = strings.Join(body, "\n")
		out.Cues = append(out.Cues, c)
	}
	if len(out.Cues) == 0 {
		return nil, errors.New("字幕文件里没有找到任何时间轴")
	}
	return out, nil
}

func secs(h, m, s, ms string) float64 {
	hh, _ := strconv.Atoi(strings.TrimSuffix(h, ":"))
	mm, _ := strconv.Atoi(m)
	ss, _ := strconv.Atoi(s)
	for len(ms) < 3 {
		ms += "0"
	}
	fr, _ := strconv.Atoi(ms)
	return float64(hh*3600+mm*60+ss) + float64(fr)/1000
}

func parseASS(text string) (*File, error) {
	out := &File{Format: ASS}
	var head []string
	format := []string{}
	inEvents := false
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			inEvents = strings.EqualFold(trim, "[Events]")
		}
		if inEvents && strings.HasPrefix(trim, "Format:") {
			for _, f := range strings.Split(strings.TrimPrefix(trim, "Format:"), ",") {
				format = append(format, strings.TrimSpace(f))
			}
		}
		if inEvents && strings.HasPrefix(trim, "Dialogue:") && len(format) > 0 {
			fields := strings.SplitN(strings.TrimPrefix(trim, "Dialogue:"), ",", len(format))
			get := func(name string) string {
				for i, f := range format {
					if strings.EqualFold(f, name) && i < len(fields) {
						return strings.TrimSpace(fields[i])
					}
				}
				return ""
			}
			c := Cue{Index: len(out.Cues) + 1, Start: assTime(get("Start")), End: assTime(get("End")), raw: line}
			c.Text = strings.ReplaceAll(get("Text"), `\N`, "\n")
			out.Cues = append(out.Cues, c)
			continue
		}
		if len(out.Cues) == 0 {
			head = append(head, line)
		}
	}
	out.header = strings.Join(head, "\n")
	if len(out.Cues) == 0 {
		return nil, errors.New("ASS 字幕里没有找到 Dialogue 行")
	}
	return out, nil
}

func assTime(s string) float64 {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	sec, _ := strconv.ParseFloat(parts[2], 64)
	return float64(h*3600+m*60) + sec
}

// Plain returns the cue text without styling tags.
func (c Cue) Plain() string {
	return strings.TrimSpace(reTag.ReplaceAllString(c.Text, ""))
}

// Write encodes f with texts replacing each cue's text (by index).
func (f *File) Write(texts map[int]string) []byte {
	var b strings.Builder
	w := bufio.NewWriter(&b)
	text := func(c Cue) string {
		if t, ok := texts[c.Index]; ok {
			return t
		}
		return c.Text
	}
	switch f.Format {
	case VTT:
		fmt.Fprintf(w, "%s\n\n", strings.TrimRight(f.header, "\n"))
		for _, c := range f.Cues {
			fmt.Fprintf(w, "%s --> %s\n%s\n\n", stamp(c.Start, "."), stamp(c.End, "."), text(c))
		}
	case ASS:
		fmt.Fprintf(w, "%s\n", strings.TrimRight(f.header, "\n"))
		for _, c := range f.Cues {
			line := c.raw
			if t, ok := texts[c.Index]; ok {
				// Text is the last field; replace everything after the last
				// format comma.
				orig := strings.ReplaceAll(c.Text, "\n", `\N`)
				if i := strings.LastIndex(line, orig); i >= 0 {
					line = line[:i] + strings.ReplaceAll(t, "\n", `\N`) + line[i+len(orig):]
				}
			}
			fmt.Fprintln(w, line)
		}
	default:
		for i, c := range f.Cues {
			fmt.Fprintf(w, "%d\n%s --> %s\n%s\n\n", i+1, stamp(c.Start, ","), stamp(c.End, ","), text(c))
		}
	}
	w.Flush()
	return []byte(b.String())
}

func stamp(sec float64, sep string) string {
	ms := int(sec*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3600000, ms/60000%60, ms/1000%60, sep, ms%1000)
}
