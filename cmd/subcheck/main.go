// Command subcheck checks a subtitle file against its video and writes a
// corrected copy:
//
//	go run ./cmd/subcheck -o fixed.srt video.mp4 subs.srt
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"flashcut/internal/clef"
	"flashcut/internal/llm"
	"flashcut/internal/media"
	"flashcut/internal/subs"
)

func main() {
	out := flag.String("o", "", "write the corrected subtitles here")
	source := flag.String("source", "auto", "check against: auto, audio or burned")
	fixer := flag.String("fixer", "@cf/qwen/qwen3-30b-a3b-fp8", "Workers AI model that fixes lines")
	vision := flag.String("vision", "@cf/qwen/qwen3.8-27b", "Workers AI model that reads burned-in subtitles")
	workers := flag.Int("j", 6, "parallel checks")
	verbose := flag.Bool("v", false, "print every cue")
	flag.Parse()
	if flag.NArg() != 2 {
		log.Fatal("usage: subcheck [-o fixed.srt] <video> <subtitles>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cf, err := clef.New(clef.Config{AccountID: os.Getenv("CLOUDFLARE_ACCOUNT_ID"), APIToken: os.Getenv("CLOUDFLARE_API_TOKEN")})
	if err != nil {
		log.Fatal(err)
	}
	info, err := media.Probe(ctx, flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	data, err := os.ReadFile(flag.Arg(1))
	if err != nil {
		log.Fatal(err)
	}
	job, err := subs.NewJob(info, flag.Arg(1), data, subs.Source(*source))
	if err != nil {
		log.Fatal(err)
	}
	workersAI := llm.Provider{ID: "cloudflare", Protocol: llm.WorkersAI}
	c := &subs.Checker{
		Clef: cf, LLM: llm.New(cf), Workers: *workers, Scratch: filepath.Join(os.TempDir(), "subcheck"),
		Fixer: subs.Model{Provider: workersAI, Name: *fixer}, Vision: subs.Model{Provider: workersAI, Name: *vision},
	}
	start := time.Now()
	err = c.Run(ctx, job, func(p subs.Progress) {
		if p.Stage != "" {
			log.Print(p.Stage)
		}
		if it := p.Item; it != nil && (*verbose || it.Status != subs.StatusOK) {
			fmt.Printf("#%d %s %s match=%.2f speech=%.2f\n  原: %s\n  听/看到: %s\n  建议: %s\n  原因: %s %s\n", it.Index, media.Clock(it.Start), it.Status, it.Match, it.Speech, it.Plain(), it.Evidence, it.Suggestion, it.Reason, it.Error)
		}
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("source=%s, %d cues in %s, %v, %d tokens", job.Source, len(job.Items), time.Since(start).Round(time.Second), job.Summary(), job.Tokens)
	if *out != "" {
		fixed, err := job.Export()
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(*out, fixed, 0o644); err != nil {
			log.Fatal(err)
		}
		log.Printf("wrote %s", *out)
	}
}
