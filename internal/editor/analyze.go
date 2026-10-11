package editor

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"flashcut/internal/clef"
	"flashcut/internal/media"
)

// Asker runs a Clef model; *clef.Client implements it.
type Asker interface {
	Run(ctx context.Context, model string, req clef.Request) (*clef.Response, error)
}

// Progress reports analysis progress.
type Progress struct {
	Done    int      `json:"done"`
	Total   int      `json:"total"`
	Segment *Segment `json:"segment,omitempty"`
}

var interestLevels = []string{
	"Skip: nothing happens, filler, waiting, ads, static or transition screens",
	"Low: routine footage with little going on",
	"Medium: somewhat interesting",
	"High: an important, exciting or informative moment",
	"Must see: one of the most memorable or important moments of the video",
}

func (p *Project) question(id string) clef.Question {
	if id == InterestID {
		return clef.Score("How worth watching is this clip for someone who only wants the best and most important parts of the whole video?", interestLevels...)
	}
	r := p.Rule(id)
	if r == nil {
		return clef.Noul("Is this clip worth keeping?", "", "")
	}
	return p.QuestionFor(*r)
}

// QuestionFor is the question the model is asked for r.
func (p *Project) QuestionFor(r Rule) clef.Question {
	if r.Question != nil {
		return *r.Question
	}
	if r.Mode == ModeDrop {
		return clef.Noul(
			fmt.Sprintf("A user editing this video said: “%s”. Does this clip contain content the user wants removed?", r.Text),
			"The clip shows what the user wants cut out", "The clip does not show it")
	}
	return clef.Noul(
		fmt.Sprintf("A user editing this video said: “%s”. Does this clip contain content the user wants to keep?", r.Text),
		"The clip shows what the user asked for", "The clip does not show it")
}

func (p *Project) state(s Segment) string {
	// Kept short: extra details such as the file name measurably skew answers.
	return fmt.Sprintf("Clip covering %s–%s of a %s video.",
		media.Clock(s.Start), media.Clock(s.End), media.Clock(p.Source.Duration))
}

// Analyze asks every segment the questions it has not answered yet, with
// up to workers requests at once. onProgress is called after each segment;
// save is called periodically so a crash or cancel keeps finished work.
func (p *Project) Analyze(ctx context.Context, ask Asker, scratch string, workers int, onProgress func(Progress), save func()) error {
	pending := p.Pending()
	if len(pending) == 0 {
		return nil
	}
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return err
	}
	var (
		mu   sync.Mutex
		done int
		wg   sync.WaitGroup
	)
	todo := make(chan int)
	workers = max(1, min(workers, 8))
	for range workers {
		wg.Go(func() {
			for i := range todo {
				mu.Lock()
				seg := p.Segments[i]
				ids := pending[i]
				mu.Unlock()
				answers, choices, tokens, err := p.judge(ctx, ask, scratch, seg, ids)
				mu.Lock()
				s := &p.Segments[i]
				if err != nil {
					if ctx.Err() == nil {
						s.Error = err.Error()
					}
				} else {
					s.Error = ""
					for k, v := range answers {
						s.Answers[k] = v
					}
					for k, v := range choices {
						if s.Choices == nil {
							s.Choices = map[string]map[string]float64{}
						}
						s.Choices[k] = v
					}
					p.Tokens += tokens
				}
				done++
				snap := *s
				snap.Answers = maps.Clone(s.Answers)
				snap.Choices = maps.Clone(s.Choices)
				prog := Progress{Done: done, Total: len(pending), Segment: &snap}
				if done%10 == 0 && save != nil {
					save()
				}
				mu.Unlock()
				if onProgress != nil {
					onProgress(prog)
				}
			}
		})
	}
	for _, s := range p.Segments {
		if _, ok := pending[s.Index]; !ok {
			continue
		}
		select {
		case todo <- s.Index:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(todo)
	wg.Wait()
	if save != nil {
		save()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	failed := 0
	var first string
	for _, s := range p.Segments {
		if s.Error != "" {
			if failed == 0 {
				first = s.Error
			}
			failed++
		}
	}
	if failed == len(pending) {
		return fmt.Errorf("所有片段都分析失败：%s", first)
	}
	return nil
}

func (p *Project) judge(ctx context.Context, ask Asker, scratch string, s Segment, ids []string) (map[string]float64, map[string]map[string]float64, int, error) {
	clip := filepath.Join(scratch, fmt.Sprintf("seg-%05d.mp4", s.Index))
	defer os.Remove(clip)
	if err := media.ExtractPreview(ctx, p.Source.Path, clip, s.Start, s.End-s.Start); err != nil {
		return nil, nil, 0, err
	}
	data, err := os.ReadFile(clip)
	if err != nil {
		return nil, nil, 0, err
	}
	if len(data) > 15<<20 {
		return nil, nil, 0, fmt.Errorf("片段预览 %d MiB，超过 16 MiB 上限", len(data)>>20)
	}
	req := clef.Request{State: p.state(s), Questions: map[string]clef.Question{}, Videos: [][]byte{data}}
	for _, id := range ids {
		req.Questions[id] = p.question(id)
	}
	resp, err := ask.Run(ctx, clef.Omni, req)
	if err != nil {
		return nil, nil, 0, err
	}
	out, choices := map[string]float64{}, map[string]map[string]float64{}
	for _, id := range ids {
		a, ok := resp.Answers[id]
		if !ok {
			return nil, nil, 0, fmt.Errorf("模型没有回答问题 %s", id)
		}
		switch a.Type {
		case "score":
			out[id] = a.Score
		case "choice":
			choices[id] = a.Probabilities
			out[id] = a.Probabilities[a.Choice]
		default:
			out[id] = a.Noul
		}
	}
	return out, choices, resp.Usage.InputTokens, nil
}
