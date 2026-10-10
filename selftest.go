package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

// selfTest drives the page like a user when LOL_HIGHLIGHTS_SELFTEST is set
// (together with LOL_HIGHLIGHTS_OPEN): it clicks 分析, then 生成集锦, and
// logs what the page shows. Used to verify the app end to end.
func selfTest(win *mygo.Window) {
	page := win.Page()
	waitFor := func(what, cond string, limit time.Duration) bool {
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			ok, err := mygo.EvalAs[bool](page, cond)
			if err == nil && ok {
				return true
			}
			if err != nil {
				log.Printf("selftest: eval: %v", err)
			}
			time.Sleep(time.Second)
		}
		log.Printf("selftest: timed out waiting for %s", what)
		return false
	}
	report := func(step string) {
		s, _ := mygo.EvalAs[string](page, `return [document.querySelector("#stats").textContent, document.querySelector("#progress span").textContent, document.querySelector("#error").textContent, document.querySelector("#out-meta").textContent].join(" | ")`)
		log.Printf("selftest %s: %s", step, s)
	}
	if !waitFor("the video to load", `return !document.querySelector("#workspace").hidden`, 30*time.Second) {
		return
	}
	start := time.Now()
	_, _ = page.Eval(`document.querySelector("#analyze").click()`)
	time.Sleep(time.Second)
	if !waitFor("analysis", `return !document.querySelector("#analyze").disabled`, 15*time.Minute) {
		return
	}
	report("analyzed in " + time.Since(start).Round(time.Second).String())
	_, _ = page.Eval(`document.querySelector("#render").click()`)
	time.Sleep(time.Second)
	if !waitFor("render", `return !document.querySelector("#render").disabled`, 15*time.Minute) {
		return
	}
	report("rendered")
	// Select a kept segment so the screenshot shows its verdict.
	_, _ = page.Eval(`const el = document.querySelector(".seg.kept"); el && el.click()`)
	time.Sleep(2 * time.Second)
	if out := os.Getenv("LOL_HIGHLIGHTS_SELFTEST"); strings.HasSuffix(out, ".png") {
		if png, err := win.CapturePage(); err != nil {
			log.Printf("selftest: capture: %v", err)
		} else if err := os.WriteFile(out, png, 0o644); err == nil {
			log.Printf("selftest: wrote %s", out)
		}
	}
}
