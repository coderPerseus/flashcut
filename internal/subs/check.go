package subs

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"flashcut/internal/clef"
	"flashcut/internal/llm"
	"flashcut/internal/media"
)

// Source says what the subtitles are checked against.
type Source string

const (
	SourceAuto   Source = "auto"   // detect burned-in subtitles, else audio
	SourceAudio  Source = "audio"  // what is said
	SourceBurned Source = "burned" // subtitles drawn into the picture
)

// Status of one cue.
type Status string

const (
	StatusPending  Status = "pending"
	StatusOK       Status = "ok"        // matches
	StatusFixed    Status = "fixed"     // wrong, with a suggested fix
	StatusSuspect  Status = "suspect"   // probably wrong, no confident fix
	StatusNoSpeech Status = "no_speech" // nothing said: timing may be off
	StatusError    Status = "error"
)

// Item is one cue and what the check found.
type Item struct {
	Cue
	Status Status `json:"status"`
	// Match is Clef-omni's probability that the cue is right.
	Match float64 `json:"match"`
	// Speech is the probability someone speaks during the cue (audio mode).
	Speech float64 `json:"speech"`
	// Evidence is what was actually heard or read on screen.
	Evidence   string `json:"evidence,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Accepted means the suggestion goes into the exported file.
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

// Job is one subtitle check, saved so it can be resumed and reviewed.
type Job struct {
	ID        string          `json:"id"`
	Video     media.VideoInfo `json:"video"`
	SubPath   string          `json:"subPath"`
	SubData   string          `json:"subData"`
	Format    Format          `json:"format"`
	Setting   Source          `json:"setting"`
	Source    Source          `json:"source"` // resolved
	Threshold float64         `json:"threshold"`
	Items     []Item          `json:"items"`
	Tokens    int             `json:"tokens"`
	UpdatedAt time.Time       `json:"updatedAt"`
	file      *File
}

// NewJob parses the subtitle file and prepares a check.
func NewJob(video media.VideoInfo, subPath string, data []byte, setting Source) (*Job, error) {
	f, err := Parse(subPath, data)
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(append([]byte(video.Path+"|"+subPath+"|"), data...))
	j := &Job{
		ID: hex.EncodeToString(sum[:8]), Video: video, SubPath: subPath, SubData: string(data), Format: f.Format,
		Setting: setting, Threshold: 0.5, file: f, UpdatedAt: time.Now(),
	}
	for _, c := range f.Cues {
		j.Items = append(j.Items, Item{Cue: c, Status: StatusPending})
	}
	return j, nil
}

// File returns the parsed subtitle file.
func (j *Job) File() (*File, error) {
	if j.file == nil {
		f, err := Parse(j.SubPath, []byte(j.SubData))
		if err != nil {
			return nil, err
		}
		j.file = f
	}
	return j.file, nil
}

// Export encodes the subtitle file with accepted suggestions applied.
func (j *Job) Export() ([]byte, error) {
	f, err := j.File()
	if err != nil {
		return nil, err
	}
	texts := map[int]string{}
	for _, it := range j.Items {
		if it.Accepted && it.Suggestion != "" {
			texts[it.Index] = it.Suggestion
		}
	}
	return f.Write(texts), nil
}

// Summary counts statuses.
func (j *Job) Summary() map[Status]int {
	out := map[Status]int{}
	for _, it := range j.Items {
		out[it.Status]++
	}
	return out
}

// Save writes the job to dir/<id>.json.
func (j *Job) Save(dir string) error {
	j.UpdatedAt = time.Now()
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, j.ID+".json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, j.ID+".json"))
}

// Load reads a saved job.
func Load(dir, id string) (*Job, error) {
	if strings.ContainsAny(id, `/\.`) {
		return nil, errors.New("invalid id")
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

// Recent lists saved jobs, newest first.
func Recent(dir string, limit int) []*Job {
	entries, _ := os.ReadDir(dir)
	var out []*Job
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			if j, err := Load(dir, id); err == nil {
				out = append(out, j)
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].UpdatedAt.After(out[b].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Model names a provider and one of its models.
type Model struct {
	Provider llm.Provider
	Name     string
}

// Checker runs the check.
type Checker struct {
	Clef    *clef.Client
	LLM     *llm.Client
	Fixer   Model // fixes wrong lines (a small, fast text model)
	Vision  Model // reads burned-in subtitles
	Workers int
	Scratch string
}

// Progress reports one finished cue.
type Progress struct {
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Item  *Item  `json:"item,omitempty"`
	Stage string `json:"stage,omitempty"`
}

// Run checks every pending cue. save is called periodically.
func (c *Checker) Run(ctx context.Context, j *Job, onProgress func(Progress), save func()) error {
	if err := os.MkdirAll(c.Scratch, 0o755); err != nil {
		return err
	}
	if j.Source == "" {
		j.Source = j.Setting
		if j.Source == "" || j.Source == SourceAuto {
			if onProgress != nil {
				onProgress(Progress{Stage: "检测画面里有没有字幕"})
			}
			src, err := c.detect(ctx, j)
			if err != nil {
				return err
			}
			j.Source = src
		}
	}
	var todo []int
	for i, it := range j.Items {
		if it.Status == StatusPending || it.Status == StatusError {
			todo = append(todo, i)
		}
	}
	var (
		mu   sync.Mutex
		done int
		wg   sync.WaitGroup
	)
	ch := make(chan int)
	for range max(1, min(c.Workers, 8)) {
		wg.Go(func() {
			for i := range ch {
				mu.Lock()
				it := j.Items[i]
				prev, next := neighbor(j, i-1), neighbor(j, i+1)
				mu.Unlock()
				res, tokens := c.checkOne(ctx, j, it, prev, next)
				mu.Lock()
				if ctx.Err() == nil {
					j.Items[i] = res
					j.Tokens += tokens
				}
				done++
				p := Progress{Done: done, Total: len(todo), Item: &res}
				if done%20 == 0 && save != nil {
					save()
				}
				mu.Unlock()
				if onProgress != nil {
					onProgress(p)
				}
			}
		})
	}
	for _, i := range todo {
		select {
		case ch <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(ch)
	wg.Wait()
	if save != nil {
		save()
	}
	return ctx.Err()
}

func neighbor(j *Job, i int) string {
	if i < 0 || i >= len(j.Items) {
		return ""
	}
	return j.Items[i].Plain()
}

// detect asks Clef-omni whether frames from a few cues show burned-in text.
func (c *Checker) detect(ctx context.Context, j *Job) (Source, error) {
	var imgs [][]byte
	n := len(j.Items)
	for k := range 4 {
		it := j.Items[(n*(2*k+1))/8]
		img, err := media.Frame(ctx, j.Video.Path, (it.Start+it.End)/2, 960)
		if err == nil {
			imgs = append(imgs, img)
		}
	}
	if len(imgs) == 0 {
		return SourceAudio, nil
	}
	resp, err := c.Clef.Run(ctx, clef.Omni, clef.Request{
		State:  "Frames taken from a video while subtitles should be showing.",
		Images: imgs,
		Questions: map[string]clef.Question{"burned": clef.Noul(
			"Is subtitle or caption text drawn into the picture of these frames (usually a line of text near the bottom)?",
			"Subtitles are visible in the picture", "No subtitles in the picture")},
	})
	if err != nil {
		return "", err
	}
	j.Tokens += resp.Usage.InputTokens
	if resp.Answers["burned"].Noul >= 0.5 {
		return SourceBurned, nil
	}
	return SourceAudio, nil
}

func (c *Checker) checkOne(ctx context.Context, j *Job, it Item, prev, next string) (Item, int) {
	it.Error, it.Evidence, it.Suggestion, it.Reason, it.Accepted = "", "", "", "", false
	text := it.Plain()
	if text == "" {
		it.Status = StatusOK
		return it, 0
	}
	// Pad the cue so words at its edges are not cut off.
	start := math.Max(0, it.Start-0.5)
	end := math.Min(j.Video.Duration, math.Max(it.End+0.5, start+1))
	end = math.Min(end, start+60)
	fail := func(err error) (Item, int) {
		it.Status, it.Error = StatusError, err.Error()
		return it, 0
	}

	req := clef.Request{State: fmt.Sprintf("A %.1f-second moment of a video, with its subtitle.", end-start), Questions: map[string]clef.Question{}}
	var frame []byte
	if j.Source == SourceBurned {
		for _, f := range []float64{0.35, 0.7} {
			img, err := media.Frame(ctx, j.Video.Path, it.Start+(it.End-it.Start)*f, 1280)
			if err != nil {
				return fail(err)
			}
			req.Images = append(req.Images, img)
			if frame == nil {
				frame = img
			}
		}
		req.Questions["match"] = clef.Noul(
			fmt.Sprintf("The subtitle file says: “%s”. Does the subtitle text shown in these frames say the same thing? Ignore punctuation, line breaks, and simplified versus traditional characters.", text),
			"The subtitle file matches the text on screen", "The subtitle file has wrong, missing or extra words compared with the screen")
	} else {
		clip := filepath.Join(c.Scratch, fmt.Sprintf("cue-%s-%05d.mp4", j.ID, it.Index))
		defer os.Remove(clip)
		if err := media.ExtractPreview(ctx, j.Video.Path, clip, start, end-start); err != nil {
			return fail(err)
		}
		data, err := os.ReadFile(clip)
		if err != nil {
			return fail(err)
		}
		req.Videos = [][]byte{data}
		req.Questions["match"] = clef.Noul(
			fmt.Sprintf("The subtitle for this moment is: “%s”. Does the speech in this clip say the same thing? Ignore punctuation, filler words, and simplified versus traditional characters.", text),
			"The subtitle matches what is said", "The subtitle has wrong words or names, or says something other than the speech")
		req.Questions["speech"] = clef.Noul("Is someone speaking in this clip?", "", "")
	}
	// Clef-omni judges the whole line; in audio mode a transcript of every
	// cue also catches single wrong words, which a yes/no judgment misses.
	var (
		resp    *clef.Response
		clefErr error
		heard   string
		asrErr  error
		wg      sync.WaitGroup
	)
	wg.Go(func() { resp, clefErr = c.Clef.Run(ctx, clef.Omni, req) })
	if j.Source == SourceBurned {
		// On-screen text is read exactly, so reading every cue is reliable.
		wg.Go(func() { heard, asrErr = c.readScreen(ctx, frame) })
	} else {
		wg.Go(func() { heard, asrErr = c.transcribe(ctx, j, it, start, end) })
	}
	wg.Wait()
	if clefErr != nil {
		return fail(clefErr)
	}
	if asrErr != nil {
		return fail(asrErr)
	}
	tokens := resp.Usage.InputTokens
	it.Match = resp.Answers["match"].Noul
	it.Speech = 1
	if a, ok := resp.Answers["speech"]; ok {
		it.Speech = a.Noul
	}
	it.Evidence = heard
	// Any real wording difference goes to the fixer; it and the verification
	// below decide whether the transcript or the subtitle is wrong.
	differs := heard != "" && Similarity(text, heard) < 0.98
	if it.Match >= j.Threshold && !differs {
		it.Status = StatusOK
		return it, tokens
	}

	// Probably wrong: find out what is really there, then fix the line.
	if j.Source != SourceBurned && strings.TrimSpace(heard) == "" && it.Speech < 0.3 {
		it.Status, it.Reason = StatusNoSpeech, "这段时间里没有人说话，可能时间轴错位了"
		return it, tokens
	}
	edits, err := c.fix(ctx, text, it.Evidence, prev, next, j.Source, j.names())
	if err != nil {
		return fail(err)
	}
	fixed, kept, rejected, risky := text, []string{}, []string{}, []string{}
	for _, e := range edits {
		if e.From == "" || !strings.Contains(fixed, e.From) || normalize(e.From) == normalize(e.To) {
			continue
		}
		// Screen text is exact: a fix must say what the screen says.
		if j.Source == SourceBurned && !strings.Contains(normalize(it.Evidence), normalize(e.To)) {
			continue
		}
		// Speech recognition is weakest on names and on the edges of a cue,
		// so edits that change Latin-script names or only delete words are
		// reported for review instead of applied.
		// Text read from the screen is exact, so only audio needs this care.
		if j.Source != SourceBurned && (reLatin.MatchString(e.From) || e.To == "") {
			desc := fmt.Sprintf("“%s”→“%s”", e.From, e.To)
			if e.To == "" {
				desc = fmt.Sprintf("删去“%s”", e.From)
			}
			risky = append(risky, desc)
			continue
		}
		// Clef-omni, which hears and sees the moment, has the last word on
		// each change, so a misheard transcript cannot overwrite a right line.
		ok, t, err := c.verify(ctx, req, e.From, e.To, j.Source)
		if err != nil {
			return fail(err)
		}
		tokens += t
		desc := fmt.Sprintf("“%s”→“%s”", e.From, e.To)
		if e.To == "" {
			desc = fmt.Sprintf("删去“%s”", e.From)
		}
		if !ok {
			rejected = append(rejected, desc)
			continue
		}
		fixed = strings.Replace(fixed, e.From, e.To, 1)
		kept = append(kept, desc+"："+e.Reason)
	}
	var notes []string
	if len(risky) > 0 {
		notes = append(notes, "可能有问题、需要你确认："+strings.Join(risky, "、"))
	}
	if len(rejected) > 0 {
		notes = append(notes, "复核没有通过："+strings.Join(rejected, "、"))
	}
	if len(kept) > 0 {
		it.Status, it.Suggestion, it.Accepted = StatusFixed, fixed, true
		it.Reason = strings.Join(append(kept, notes...), "；")
		return it, tokens
	}
	it.Reason = strings.Join(notes, "；")
	if len(edits) == 0 {
		it.Reason = "复查后没有发现需要修改的地方"
	}
	if len(risky) > 0 {
		it.Status = StatusSuspect
		return it, tokens
	}
	if it.Match < 0.25 {
		it.Status = StatusSuspect
		if it.Reason == "" {
			it.Reason = "模型判断不匹配，但没有给出可靠的修改"
		}
	} else {
		it.Status = StatusOK
	}
	return it, tokens
}

var (
	reSpace = regexp.MustCompile(`[\s\p{P}]+`)
	reHan   = regexp.MustCompile(`\p{Han}`)
)

func normalize(s string) string { return reSpace.ReplaceAllString(strings.ToLower(s), "") }

func (c *Checker) transcribe(ctx context.Context, j *Job, it Item, start, end float64) (string, error) {
	audio := filepath.Join(c.Scratch, fmt.Sprintf("cue-%s-%05d.mp3", j.ID, it.Index))
	defer os.Remove(audio)
	if err := media.ExtractAudio(ctx, j.Video.Path, audio, start, end-start); err != nil {
		return "", err
	}
	data, err := os.ReadFile(audio)
	if err != nil {
		return "", err
	}
	body := map[string]any{"audio": base64.StdEncoding.EncodeToString(data), "task": "transcribe", "vad_filter": true}
	// Never prompt with the cue itself: Whisper would copy its mistakes.
	// For Chinese, a neutral prompt keeps the output in simplified script.
	prompt := ""
	if reHan.MatchString(it.Plain()) {
		body["language"] = "zh"
		prompt = "以下是普通话的句子。"
	}
	if prompt != "" {
		body["initial_prompt"] = prompt
	}
	out, err := c.Clef.RunRaw(ctx, "@cf/openai/whisper-large-v3-turbo", body)
	if err != nil {
		return "", fmt.Errorf("语音识别失败：%w", err)
	}
	var r struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return "", fmt.Errorf("语音识别结果无法解析：%.200s", out)
	}
	return strings.TrimSpace(r.Text), nil
}

func (c *Checker) readScreen(ctx context.Context, frame []byte) (string, error) {
	resp, err := c.LLM.Chat(ctx, c.Vision.Provider, c.Vision.Name, llm.Request{
		MaxTokens: 200,
		Messages: []llm.Message{{Role: "user", Images: [][]byte{frame},
			Content: `Read the subtitle (caption) text drawn into this video frame, exactly as written. Reply with JSON {"subtitle": "..."}; use "" when there is none.`}},
	})
	if err != nil {
		return "", fmt.Errorf("读取画面字幕失败：%w", err)
	}
	var r struct {
		Subtitle string `json:"subtitle"`
	}
	if err := json.Unmarshal([]byte(firstJSON(resp.Text)), &r); err != nil {
		return strings.TrimSpace(resp.Text), nil
	}
	return strings.TrimSpace(r.Subtitle), nil
}

// Edit replaces From (an exact part of the line) with To.
type Edit struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// verify asks Clef-omni whether the moment has to (not from).
func (c *Checker) verify(ctx context.Context, moment clef.Request, from, to string, src Source) (bool, int, error) {
	what := "is actually said in this clip"
	if src == SourceBurned {
		what = "is actually written in the subtitle shown in these frames"
	}
	b := to
	if b == "" {
		b = "(nothing: these words are not there)"
	}
	moment.Questions = map[string]clef.Question{"pick": clef.Choice(
		fmt.Sprintf("Which of these %s?", what),
		map[string]string{"a": from, "b": b, "neither": "Neither of them"})}
	resp, err := c.Clef.Run(ctx, clef.Omni, moment)
	if err != nil {
		return false, 0, err
	}
	p := resp.Answers["pick"].Probabilities
	return p["b"] >= 0.5 && p["b"] > p["a"]+0.15, resp.Usage.InputTokens, nil
}

var reLatin = regexp.MustCompile(`[A-Za-z][A-Za-z0-9.'\-]{1,}`)

// names lists Latin-script words used in the file (player and team names,
// terms), which speech recognition tends to spell differently.
func (j *Job) names() []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range j.Items {
		for _, w := range reLatin.FindAllString(it.Text, -1) {
			if !seen[w] && len(out) < 40 {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

func (c *Checker) fix(ctx context.Context, original, evidence, prev, next string, src Source, names []string) ([]Edit, error) {
	what := "a speech-recognition transcript (it may mishear names, and may use traditional characters)"
	if src == SourceBurned {
		what = "the subtitle text read from the video frame"
	}
	prompt := fmt.Sprintf(`You proofread video subtitles against what is really in the video. The evidence below is %s of the same moment.
List the real mistakes in the subtitle line as small edits: a wrong word, a wrong number, a wrong or misspelled name, or a sentence that says something different from the evidence. Each edit replaces "from" (copied exactly from the subtitle line) with "to". Keep the subtitle's language and script: if the subtitle uses simplified characters, write simplified even when the evidence is traditional. Do not edit punctuation, filler words, or wording that means the same. The evidence may miss or add words at its very start or end and may mishear names as Chinese sound-alikes; never edit only because of that, and never swap one name for another unless the evidence clearly says that name.

Names and terms used elsewhere in these subtitles (the transcript may write them as Chinese sound-alikes; keep the subtitle's spelling): %s

Previous line: %q
Subtitle line: %q
Next line: %q
Evidence: %q

Reply with JSON only: {"edits": [{"from": "...", "to": "...", "reason": "a short reason in Chinese"}]}; use {"edits": []} when the line is right.`, what, strings.Join(names, ", "), prev, original, next, evidence)
	resp, err := c.LLM.Chat(ctx, c.Fixer.Provider, c.Fixer.Name, llm.Request{
		MaxTokens: 300, JSON: c.Fixer.Provider.Protocol != llm.Anthropic,
		Messages: []llm.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return nil, fmt.Errorf("修正字幕失败：%w", err)
	}
	var r struct {
		Edits []Edit `json:"edits"`
	}
	if err := json.Unmarshal([]byte(firstJSON(resp.Text)), &r); err != nil {
		return nil, fmt.Errorf("修正结果无法解析：%.200s", resp.Text)
	}
	return r.Edits, nil
}

// firstJSON returns the first {...} object in s.
func firstJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return s
	}
	return s[start : end+1]
}

// Similarity compares two lines by edit distance, ignoring punctuation and
// spaces: 1 is identical, 0 is completely different.
func Similarity(a, b string) float64 {
	ra, rb := []rune(normalize(a)), []rune(normalize(b))
	if len(ra) == 0 && len(rb) == 0 {
		return 1
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}
