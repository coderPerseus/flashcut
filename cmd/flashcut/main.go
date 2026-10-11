// Command flashcut edits a video from the command line, the same way the
// app's chat does:
//
//	go run ./cmd/flashcut -m "只保留击杀和团战" -m "控制在5分钟以内" -o out.mp4 video.mp4
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"flashcut/internal/clef"
	"flashcut/internal/editor"
	"flashcut/internal/effects"
	"flashcut/internal/media"
)

type messages []string

func (m *messages) String() string     { return strings.Join(*m, " | ") }
func (m *messages) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var msgs messages
	flag.Var(&msgs, "m", "an editing request, as typed in the chat (repeatable)")
	out := flag.String("o", "", "export to this file (default: only print the selection)")
	work := flag.String("work", filepath.Join(os.TempDir(), "flashcut"), "where projects are kept")
	seg := flag.Float64("seg", editor.DefaultSegmentSeconds, "segment length in seconds for new projects")
	workers := flag.Int("j", 4, "parallel model requests")
	quality := flag.String("q", string(media.QualityMedium), "export quality: high, medium or small")
	fx := flag.String("fx", "none", "motion graphics: none, esports or clean")
	title := flag.String("title", "", "title for the intro and end card (default: file name)")
	fxDir := flag.String("fx-dir", filepath.Join(os.TempDir(), "flashcut-effects"), "where the Remotion engine is installed")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: flashcut [-m request]... [-o out.mp4] <video>")
	}
	src, _ := filepath.Abs(flag.Arg(0))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := clef.New(clef.Config{AccountID: os.Getenv("CLOUDFLARE_ACCOUNT_ID"), APIToken: os.Getenv("CLOUDFLARE_API_TOKEN")})
	if err != nil {
		log.Fatal(err)
	}
	st, err := os.Stat(src)
	if err != nil {
		log.Fatal(err)
	}
	info, err := media.Probe(ctx, src)
	if err != nil {
		log.Fatal(err)
	}
	id := editor.ProjectID(info, st.ModTime())
	p, err := editor.Load(*work, id)
	if err != nil {
		p = editor.New(id, info, *seg)
	}
	save := func() {
		if err := p.Save(*work); err != nil {
			log.Print(err)
		}
	}
	analyze := func() {
		if len(p.Pending()) == 0 {
			return
		}
		start := time.Now()
		err := p.Analyze(ctx, client, filepath.Join(*work, id, "scratch"), *workers, func(pr editor.Progress) {
			if pr.Done%20 == 0 || pr.Done == pr.Total {
				log.Printf("analyzed %d/%d", pr.Done, pr.Total)
			}
		}, save)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("analysis took %s", time.Since(start).Round(time.Second))
	}
	analyze()
	for _, m := range msgs {
		in, _, err := editor.Understand(ctx, client, m)
		if err != nil {
			log.Fatal(err)
		}
		o := p.Apply(m, in)
		log.Printf("%q → %+v\n  %s", m, in, o.Reply)
		if o.Analyze {
			analyze()
		}
	}
	p.Select()
	save()
	sum := p.Summarize()
	log.Printf("kept %d/%d segments, %s in %d clips, %d tokens ≈ $%.2f", sum.KeptSegments, sum.Segments, media.Clock(sum.KeptSeconds), sum.Clips, sum.Tokens, sum.CostUSD)
	for _, r := range p.Ranges() {
		fmt.Printf("  %s – %s\n", media.Clock(r.Start), media.Clock(r.End))
	}
	if *out == "" {
		return
	}
	cut := *out
	if *fx != "none" {
		cut = strings.TrimSuffix(*out, filepath.Ext(*out)) + ".cut.mp4"
		defer os.Remove(cut)
	}
	start := time.Now()
	if err := media.Render(ctx, src, cut, p.Ranges(), media.Quality(*quality), nil); err != nil {
		log.Fatal(err)
	}
	log.Printf("cut in %s", time.Since(start).Round(time.Second))
	if *fx != "none" {
		info, err := media.Probe(ctx, cut)
		if err != nil {
			log.Fatal(err)
		}
		s := editor.DefaultEffects()
		s.Style, s.Title = *fx, *title
		plan := effects.BuildPlan(p, s, info)
		log.Printf("%d effects", len(plan.Events))
		start = time.Now()
		last := ""
		err = effects.Engine{Dir: *fxDir}.Render(ctx, cut, plan, *out, func(stage string, pr float64) {
			if line := fmt.Sprintf("%s %d%%", stage, int(pr*10)*10); line != last {
				last = line
				log.Print(line)
			}
		})
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("effects in %s", time.Since(start).Round(time.Second))
	}
	log.Printf("wrote %s", *out)
}
