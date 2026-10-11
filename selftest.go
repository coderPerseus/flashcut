package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

// selfTest drives the app like a user, for end-to-end checks:
//
//	FLASHCUT_OPEN=video.mp4            the video to open
//	FLASHCUT_SELFTEST=shot.png         where to save a screenshot at the end
//	FLASHCUT_SELFTEST_CHAT="a|b|c"     chat messages to send, in order
//	FLASHCUT_SELFTEST_EXPORT=out.mp4   export here (skipping the save dialog)
func selfTest(win *mygo.Window, studio *Studio) {
	page := win.Page()
	eval := func(js string) string {
		v, err := mygo.EvalAs[string](page, js)
		if err != nil {
			return "error: " + err.Error()
		}
		return v
	}
	waitFor := func(what, cond string, limit time.Duration) bool {
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			if ok, err := mygo.EvalAs[bool](page, cond); err == nil && ok {
				return true
			}
			time.Sleep(time.Second)
		}
		log.Printf("selftest: timed out waiting for %s; page: %s", what,
			eval(`return (document.querySelector("#error")?.textContent ?? "") + " :: " + document.body.innerText.slice(0, 300)`))
		return false
	}
	idle := `return !!window.flashcut && !window.flashcut.busy()`
	if !waitFor("the video", `return !!window.flashcut && !!window.flashcut.projectId()`, time.Minute) {
		return
	}
	for _, msg := range strings.Split(os.Getenv("FLASHCUT_SELFTEST_CHAT"), "|") {
		if msg = strings.TrimSpace(msg); msg == "" {
			continue
		}
		start := time.Now()
		js, _ := json.Marshal(msg)
		eval(`window.flashcut.send(` + string(js) + `); return ""`)
		time.Sleep(time.Second)
		if !waitFor("chat reply", idle, 20*time.Minute) {
			return
		}
		log.Printf("selftest chat %q (%s): %s", msg, time.Since(start).Round(time.Second),
			eval(`return window.flashcut.lastReply() + " || " + document.querySelector("#summary").textContent`))
	}
	if out := os.Getenv("FLASHCUT_SELFTEST_EXPORT"); out != "" {
		id := eval(`return window.flashcut.projectId()`)
		e, err := studio.entry(id)
		if err != nil {
			log.Printf("selftest: %v", err)
			return
		}
		e.mu.Lock()
		res, err := studio.export(context.Background(), e.p, out, nil)
		e.mu.Unlock()
		if err != nil {
			log.Printf("selftest: export: %v", err)
		} else {
			log.Printf("selftest: exported %s (%.0fs, %d bytes)", res.Path, res.Duration, res.Size)
		}
	}
	if shot := os.Getenv("FLASHCUT_SELFTEST"); strings.HasSuffix(shot, ".png") {
		eval(`window.flashcut.selectFirstKept(); return ""`)
		time.Sleep(2 * time.Second)
		if png, err := win.CapturePage(); err != nil {
			log.Printf("selftest: capture: %v", err)
		} else if err := os.WriteFile(shot, png, 0o644); err == nil {
			log.Printf("selftest: wrote %s", shot)
		}
	}
	log.Printf("selftest: done")
}
