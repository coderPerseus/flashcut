package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"flashcut/internal/editor"
)

// selfTest drives the app like a user, for end-to-end checks:
//
//	FLASHCUT_OPEN=video.mp4            the video to open
//	FLASHCUT_SELFTEST=shot.png         where to save a screenshot at the end
//	FLASHCUT_SELFTEST_CHAT="a|b|c"     chat messages to send, in order
//	FLASHCUT_SELFTEST_EXPORT=out.mp4   export here (skipping the save dialog)
//	FLASHCUT_SELFTEST_TEMPLATE=id      apply this template first
//	FLASHCUT_SELFTEST_MANAGER=m.png    screenshot the template manager at the end
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
	if id := os.Getenv("FLASHCUT_SELFTEST_TEMPLATE"); id != "" {
		start := time.Now()
		js, _ := json.Marshal(id)
		eval(`document.querySelector("#tpl-select").value = ` + string(js) + `; document.querySelector("#tpl-apply").click(); return ""`)
		time.Sleep(time.Second)
		if !waitFor("template", idle, 20*time.Minute) {
			return
		}
		log.Printf("selftest template %s (%s): %s", id, time.Since(start).Round(time.Second),
			eval(`return window.flashcut.lastReply() + " || " + document.querySelector("#summary").textContent`))
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
		fx := editor.DefaultEffects()
		if style := os.Getenv("FLASHCUT_SELFTEST_FX"); style != "" {
			fx.Style = style
		}
		res, err := studio.export(context.Background(), e.p, out, fx, nil)
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
	if dir := os.Getenv("FLASHCUT_SELFTEST_SHOTS"); dir != "" {
		capture := func(name string) {
			time.Sleep(1500 * time.Millisecond)
			if png, err := win.CapturePage(); err == nil {
				_ = os.WriteFile(filepath.Join(dir, name), png, 0o644)
				log.Printf("selftest: wrote %s", name)
			}
		}
		capture("edit.png")
		log.Printf("selftest hover heights: %s", eval(`const tl = document.querySelector("#timeline"); const h0 = tl.getBoundingClientRect().height; const segs = [...document.querySelectorAll(".seg")]; const hs = []; for (const i of [5, 60, 120, 180]) { segs[i].dispatchEvent(new MouseEvent("mouseenter")); hs.push(tl.getBoundingClientRect().height); segs[i].dispatchEvent(new MouseEvent("mouseleave")); hs.push(tl.getBoundingClientRect().height); } return h0 + " -> " + hs.join(",")`))
		eval(`window.flashcut.zoom(12, 1145.3, 1158.8); return ""`)
		capture("timeline-zoom.png")
		eval(`document.querySelector("#tl-fit").click(); return ""`)
		eval(`document.querySelector('#legend [data-filter="dropped"]').click(); return ""`)
		capture("filter.png")
		eval(`document.querySelector('#legend [data-filter="dropped"]').click(); return ""`)
		eval(`window.flashcut.openRules(); return ""`)
		capture("rules.png")
		eval(`window.flashcut.openRules(); window.flashcut.openSettings(); return ""`)
		time.Sleep(3 * time.Second)
		capture("settings.png")
		eval(`document.querySelector("#settings").close(); return ""`)
		if pair := strings.SplitN(os.Getenv("FLASHCUT_SELFTEST_SUBS"), "|", 2); len(pair) == 2 {
			v, _ := json.Marshal(pair[0])
			sub, _ := json.Marshal(pair[1])
			eval(`window.flashcut.subs(` + string(v) + `, ` + string(sub) + `); return ""`)
			time.Sleep(3 * time.Second)
			if waitFor("subtitle check", `return !window.flashcut.subsBusy()`, 15*time.Minute) {
				log.Printf("selftest subs: %s", eval(`return document.querySelector("#subs-summary").textContent`))
				eval(`window.flashcut.selectIssue(); return ""`)
				capture("subs.png")
			}
		}
	}
	if shot := os.Getenv("FLASHCUT_SELFTEST_MANAGER"); strings.HasSuffix(shot, ".png") {
		eval(`document.querySelector("#tpl-manage").click(); return ""`)
		time.Sleep(2 * time.Second)
		if png, err := win.CapturePage(); err == nil {
			_ = os.WriteFile(shot, png, 0o644)
			log.Printf("selftest: wrote %s", shot)
		}
	}
	log.Printf("selftest: done")
}
