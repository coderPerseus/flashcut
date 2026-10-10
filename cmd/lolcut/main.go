// Command lolcut runs the highlight pipeline without the app window:
//
//	go run ./cmd/lolcut -o out.mp4 match.mp4
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

	"lol-highlights/internal/highlight"
)

func main() {
	def := highlight.DefaultOptions()
	out := flag.String("o", "", "output file (default: <input>-highlights.mp4)")
	work := flag.String("work", filepath.Join(os.TempDir(), "lol-highlights"), "cache directory for analysis")
	flag.Float64Var(&def.SegmentSeconds, "seg", def.SegmentSeconds, "segment length in seconds")
	flag.IntVar(&def.Concurrency, "j", def.Concurrency, "parallel model requests")
	flag.Float64Var(&def.KeepThreshold, "keep", def.KeepThreshold, "keep when P(keep) ≥ this")
	flag.Float64Var(&def.ScoreThreshold, "score", def.ScoreThreshold, "keep when score (0-4) ≥ this")
	analyzeOnly := flag.Bool("analyze-only", false, "judge segments and print the selection, but do not render")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: lolcut [flags] <video>")
	}
	src, _ := filepath.Abs(flag.Arg(0))
	if *out == "" {
		*out = strings.TrimSuffix(src, filepath.Ext(src)) + "-highlights.mp4"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	judge, err := highlight.NewJudge()
	if err != nil {
		log.Fatal(err)
	}
	wd := highlight.WorkDir(*work, src)
	log.Printf("judge: %s, cache: %s", judge.Name(), wd)
	a, err := highlight.Analyze(ctx, judge, src, wd, def, func(p highlight.Progress) {
		if s := p.Segment; s != nil && s.Verdict != nil {
			fmt.Printf("[%3d/%d] %s  keep=%.2f score=%.2f %s\n", p.Done, p.Total, highlight.Clock(s.Start), s.Verdict.Keep, s.Verdict.Score, s.Verdict.Scene)
		} else if s != nil {
			fmt.Printf("[%3d/%d] %s  ERROR %s\n", p.Done, p.Total, highlight.Clock(s.Start), s.Error)
		}
	})
	if err != nil {
		log.Fatal(err)
	}
	ranges := a.Select(def)
	_ = a.Save(wd)
	st := a.Stats()
	log.Printf("kept %d/%d segments, %s of %s, %d failed, %d input tokens, scenes: %s",
		st.Kept, st.Segments, highlight.Clock(st.KeptSeconds), highlight.Clock(st.TotalSeconds), st.Failed, st.Tokens, strings.Join(st.TopScenes(), " "))
	for _, r := range ranges {
		fmt.Printf("  %s – %s\n", highlight.Clock(r.Start), highlight.Clock(r.End))
	}
	if *analyzeOnly {
		return
	}
	if err := highlight.Render(ctx, src, *out, ranges, func(d, t int) { log.Printf("rendered part %d/%d", d, t) }); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s", *out)
}
