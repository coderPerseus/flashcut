import { Channel, onFileDrop } from "mygo-runtime";
import { Studio, type EffectSettings, type Event, type ExportResult, type Range, type Segment, type View } from "./mygo";
import { iconButton, icons } from "./icons";
import { initLayout, isShown, togglePanel, type PanelName } from "./layout";
import { initSettings, openSettings } from "./settings";
import { examplesFor, initTemplates, openManager, renderPicker } from "./templates";
import { initSubs, subsDrop, subsHome, subsToolbar } from "./subtitles";
import { $, attempt, clock, el, escapeHTML, mb, toast } from "./ui";
import { clearSelection, consumeClick, fit, getSelection, initTimeline, resetTimeline, selectRange, setPlayhead, setZoom, type Selection } from "./timeline";

let view: View | null = null;
let selected = -1;
let busy: "" | "chat" | "analyze" | "export" = "";
let running: Channel<Event> | null = null;
let pendingText = "";
let lastExport: ExportResult | null = null;
let mode: "edit" | "subs" = "edit";
type SegFilter = "" | "kept" | "dropped" | "pending" | "failed";
let segFilter: SegFilter = "";

function segStatus(s: Segment): Exclude<SegFilter, ""> {
  if (s.error) return "failed";
  if (s.answers?.interest === undefined) return "pending";
  return s.kept ? "kept" : "dropped";
}
const project = () => view?.project ?? null;
const MODE_LABELS: Record<string, string> = { keep: "保留", drop: "去掉", boost: "加分", marker: "节点", opening: "开局", ending: "结尾" };
const VIDEO_EXT = /\.(mp4|mov|m4v|mkv|webm|avi|flv|ts|mts|wmv|mpg|mpeg)$/i;
const SUB_EXT = /\.(srt|vtt|ass|ssa)$/i;

// ---- toolbar ----

function panelToggle(name: PanelName, icon: "sidebarLeft" | "sidebarRight" | "panelBottom", title: string) {
  const b = iconButton(icon, title);
  b.classList.toggle("on", isShown(name));
  b.onclick = () => b.classList.toggle("on", togglePanel(name));
  return b;
}

function renderToolbar() {
  const bar = $("#toolbar");
  const sep = () => el("span", { class: "sep" });
  const gear = iconButton("gear", "设置（⌘,）");
  gear.onclick = () => openSettings();
  if (mode === "subs") {
    bar.replaceChildren(...subsToolbar(), sep(), panelToggle("subs-left", "sidebarLeft", "显示 / 隐藏字幕列表"), panelToggle("subs-right", "sidebarRight", "显示 / 隐藏详情"), sep(), gear);
    return;
  }
  if (!project()) {
    const open = iconButton("open", "打开视频（⌘O）");
    open.onclick = pick;
    bar.replaceChildren(open, sep(), gear);
    return;
  }
  const open = iconButton("open", "打开视频（⌘O）");
  open.onclick = pick;
  const undoBtn = iconButton("undo", "撤销（⌘Z）");
  undoBtn.id = "undo";
  undoBtn.onclick = undo;
  const rulesBtn = iconButton("sliders", "剪辑要求");
  rulesBtn.id = "rules-btn";
  rulesBtn.onclick = () => toggleRules(rulesBtn);
  const tpl = iconButton("template", "模板");
  tpl.onclick = () => openManager();
  const exp = el("button", { id: "export", title: "导出成片（⌘E）" });
  exp.innerHTML = `${icons.export}<span style="margin-left:4px">导出</span>`;
  exp.style.display = "inline-flex";
  exp.style.alignItems = "center";
  exp.onclick = exportVideo;
  bar.replaceChildren(
    open, undoBtn, sep(), rulesBtn, tpl, sep(),
    panelToggle("edit-left", "sidebarLeft", "显示 / 隐藏片段列表"),
    panelToggle("edit-bottom", "panelBottom", "显示 / 隐藏时间线"),
    panelToggle("edit-right", "sidebarRight", "显示 / 隐藏聊天"),
    sep(), gear, exp,
  );
  renderBusy();
}

function setMode(m: "edit" | "subs") {
  mode = m;
  for (const b of document.querySelectorAll<HTMLButtonElement>(".segmented button")) b.classList.toggle("active", b.dataset.mode === m);
  $("#subs").hidden = m !== "subs";
  $("#edit").hidden = m !== "edit" || !project();
  $("#home").hidden = m !== "edit" || !!project();
  $("#rules-pop").hidden = true;
  $("#project-name").textContent = m === "edit" && project() ? `${project()!.source.path.split("/").pop()} · ${clock(project()!.source.duration)}` : "";
  if (m === "subs") subsHome();
  renderToolbar();
}
for (const b of document.querySelectorAll<HTMLButtonElement>(".segmented button")) b.onclick = () => setMode(b.dataset.mode as "edit" | "subs");

// ---- rules popover ----

function toggleRules(anchor: HTMLElement) {
  const pop = $("#rules-pop");
  if (!pop.hidden) {
    pop.hidden = true;
    return;
  }
  const r = anchor.getBoundingClientRect();
  pop.hidden = false;
  pop.style.top = `${r.bottom + 8}px`;
  pop.style.left = `${Math.max(12, Math.min(window.innerWidth - pop.offsetWidth - 12, r.right - pop.offsetWidth / 2))}px`;
}
document.addEventListener("pointerdown", (e) => {
  const pop = $("#rules-pop");
  const t = e.target as HTMLElement;
  if (!pop.hidden && !pop.contains(t) && !t.closest("#rules-btn") && !t.closest("dialog")) pop.hidden = true;
});

// ---- home ----

async function showHome() {
  view = null;
  setMode("edit");
  const recent = ((await attempt(() => Studio.recent())) ?? []).filter((r) => !!r);
  const box = $("#recent");
  box.replaceChildren();
  if (recent.length) box.append(el("h3", { textContent: "最近的剪辑" }));
  for (const r of recent) {
    const forget = el("button", { class: "small", textContent: "移除" });
    forget.onclick = async (e) => {
      e.stopPropagation();
      await attempt(() => Studio.forget(r!.id));
      showHome();
    };
    const item = el(
      "div",
      { class: "recent-item" + (r!.missing ? " missing" : "") },
      el(
        "div",
        {},
        el("div", { class: "name", textContent: r!.name }),
        el("div", { class: "meta", textContent: r!.missing ? "原视频已移动或删除" : `${clock(r!.duration)} → 成片 ${clock(r!.keptSeconds)} · ${new Date(r!.updatedAt).toLocaleString()}` }),
      ),
      forget,
    );
    if (!r!.missing) item.onclick = async () => openView(await attempt(() => Studio.open(r!.id)));
    box.append(item);
  }
  const subsRecent = ((await attempt(() => Studio.subsRecent())) ?? []).filter((r) => !!r?.job);
  const sbox = $("#recent-subs");
  sbox.replaceChildren();
  if (subsRecent.length) sbox.append(el("h3", { textContent: "最近的字幕校对" }));
  for (const r of subsRecent) {
    const j = r!.job!;
    const item = el(
      "div",
      { class: "recent-item" },
      el("div", {}, el("div", { class: "name", textContent: j.subPath.split("/").pop() }), el("div", { class: "meta", textContent: `${j.video.path.split("/").pop()} · ${new Date(j.updatedAt).toLocaleString()}` })),
    );
    item.onclick = () => {
      setMode("subs");
      subsDrop([], j.id);
    };
    sbox.append(item);
  }
  renderConnection();
}

async function pick() {
  if (busy) return toast("请等当前操作完成");
  const v = await attempt(() => Studio.pickVideo());
  if (v) openView(v);
}
$("#open-big").onclick = pick;
$("#subs-big").onclick = () => setMode("subs");

function openView(v: View | null | undefined) {
  if (!v?.project) return;
  selected = -1;
  lastExport = null;
  view = v;
  setMode("edit");
  $<HTMLVideoElement>("#video").src = v.mediaUrl;
  setView(v);
  resetTimeline();
  $("#input").focus();
}

// ---- rendering ----

function setView(v: View | null | undefined) {
  if (!v?.project) return;
  view = v;
  renderSummary();
  renderTimeline();
  renderRanges();
  renderClips();
  renderRules();
  renderOptions();
  renderMessages(busy === "chat" ? pendingText : "");
  renderChips();
  renderBusy();
  renderPicker();
  showDetail(v.project.segments[selected]);
}

function renderSummary() {
  if (!view) return;
  const s = view.summary;
  const parts = [`成片 ${clock(s.keptSeconds)} / 原片 ${clock(project()!.source.duration)}`, `${s.clips} 个片段`];
  if (s.analyzed < s.segments) parts.push(`已分析 ${s.analyzed}/${s.segments}`);
  if (s.failed) parts.push(`失败 ${s.failed}`);
  if (s.tokens) parts.push(`已花费 ≈ $${s.costUsd.toFixed(2)}`);
  $("#summary").textContent = parts.join(" · ");
}

function renderTimeline() {
  const p = project();
  if (!p) return;
  const bars = p.segments.map((s) => {
    const seg = el("div", { class: "seg" });
    const bar = el("i");
    const interest = s.answers?.interest;
    if (s.error) seg.classList.add("failed");
    else if (interest === undefined) seg.classList.add("pending");
    else bar.style.height = `${Math.max(4, (interest / 4) * 100)}%`;
    if (s.kept) seg.classList.add("kept");
    if (p.edits.overrides?.[s.index] !== undefined) seg.classList.add("pinned");
    if (s.index === selected) seg.classList.add("selected");
    if (segFilter && segStatus(s) !== segFilter) seg.classList.add("dim");
    seg.append(bar);
    seg.title = `${clock(s.start)} – ${clock(s.end)}${s.reason ? " · " + s.reason : ""}`;
    seg.onmouseenter = () => showDetail(s);
    seg.onmouseleave = () => showDetail(project()?.segments[selected]);
    seg.onclick = () => {
      if (!consumeClick()) selectSegment(s.index, true);
    };
    return seg;
  });
  $("#timeline").replaceChildren(...bars);
  renderLegend();
}

function renderRanges() {
  const p = project();
  if (!p || !view) return;
  $("#ranges-bar").replaceChildren(
    ...view.ranges.map((r) => {
      const s = el("span");
      s.style.left = `${(r.start / p.source.duration) * 100}%`;
      s.style.width = `${((r.end - r.start) / p.source.duration) * 100}%`;
      return s;
    }),
  );
}

function renderLegend() {
  const p = project();
  const counts: Record<string, number> = { kept: 0, dropped: 0, pending: 0, failed: 0 };
  for (const s of p?.segments ?? []) counts[segStatus(s)]!++;
  for (const b of document.querySelectorAll<HTMLButtonElement>("#legend button")) {
    const f = b.dataset.filter!;
    b.classList.toggle("on", segFilter === f);
    b.querySelector("b")!.textContent = counts[f] ? String(counts[f]) : "";
  }
}

for (const b of document.querySelectorAll<HTMLButtonElement>("#legend button")) {
  b.onclick = () => {
    segFilter = segFilter === b.dataset.filter ? "" : (b.dataset.filter as SegFilter);
    renderTimeline();
    renderClips();
  };
}

/** renderFilteredSegments lists the segments the legend filter picks. */
function renderFilteredSegments() {
  const p = project()!;
  const names: Record<string, string> = { kept: "保留", dropped: "剪掉", pending: "待分析", failed: "失败" };
  const list = p.segments.filter((s) => segStatus(s) === segFilter);
  $("#clip-count").textContent = `${names[segFilter]} ${list.length}`;
  const video = $<HTMLVideoElement>("#video");
  $("#clips").replaceChildren(
    ...(list.length
      ? list.map((s) => {
          const row = el(
            "div",
            { class: "row-item" + (s.index === selected ? " active" : "") },
            el("div", { class: "time" }, el("span", { class: `dot ${segStatus(s) === "kept" ? "fixed" : segStatus(s) === "failed" ? "error" : segStatus(s)}` }), `${clock(s.start)} – ${clock(s.end)}`),
            el("div", { class: "text muted", textContent: s.error || s.reason || "" }),
          );
          row.onclick = () => {
            selectSegment(s.index, true);
            video.currentTime = s.start + 0.01;
            renderFilteredSegments();
          };
          return row;
        })
      : [el("div", { class: "empty-note", textContent: `没有${names[segFilter]}的段。` })]),
  );
}

function renderClips() {
  const p = project();
  if (!p || !view) return;
  if (segFilter) {
    renderFilteredSegments();
    return;
  }
  $("#clip-count").textContent = view.ranges.length ? `${view.ranges.length}` : "";
  const video = $<HTMLVideoElement>("#video");
  const items = view.ranges.map((r, i) => {
    const row = el(
      "div",
      { class: "row-item" },
      el("div", { class: "time" }, el("span", { class: "dot fixed" }), `#${i + 1}  ${clock(r.start)} – ${clock(r.end)}  ·  ${Math.round(r.end - r.start)} 秒`),
      el("div", { class: "text muted", textContent: reasonAt(r) }),
    );
    row.onclick = () => {
      video.currentTime = r.start + 0.01;
      for (const x of document.querySelectorAll("#clips .row-item")) x.classList.remove("active");
      row.classList.add("active");
    };
    return row;
  });
  $("#clips").replaceChildren(...(items.length ? items : [el("div", { class: "empty-note", textContent: "还没有保留的片段。在聊天里说说要求，或点工具栏的剪辑要求 → 智能剪辑。" })]));
}

/** reasonAt explains a clip by its first kept segment. */
function reasonAt(r: Range) {
  const s = project()?.segments.find((s) => s.kept && s.end > r.start + 0.01 && s.start < r.end);
  return s?.reason ?? "";
}

function showDetail(s: Segment | undefined) {
  const box = $("#detail");
  const p = project();
  $("#seg-actions").hidden = !(s && s.index === selected);
  if (!s || !p) {
    box.textContent = "点击时间线上的一段可以跳转预览，并手动保留或剪掉。";
    return;
  }
  const parts = [`<b>${clock(s.start)} – ${clock(s.end)}</b>`, s.kept ? "<b>保留</b>" : "剪掉"];
  if (s.error) parts.push(`分析失败：${escapeHTML(s.error)}`);
  const interest = s.answers?.interest;
  if (interest !== undefined) parts.push(`精彩程度 <b>${interest.toFixed(1)}</b>/4`);
  for (const r of p.edits.rules) {
    const v = s.answers?.[r.id];
    if (v !== undefined) {
      const shown = r.question?.type === "score" ? v.toFixed(1) : `${Math.round(v * 100)}%`;
      parts.push(`${MODE_LABELS[r.mode] ?? r.mode}“${escapeHTML(r.text.length > 16 ? r.text.slice(0, 15) + "…" : r.text)}” <b>${shown}</b>`);
    }
  }
  if (s.reason) parts.push(escapeHTML(s.reason));
  box.innerHTML = parts.join(" · ");
}

function selectSegment(index: number, seek: boolean) {
  const p = project();
  if (!p) return;
  selected = index;
  const s = p.segments[index];
  if (seek && s) $<HTMLVideoElement>("#video").currentTime = s.start + 0.01;
  renderTimeline();
  showDetail(s);
}

for (const btn of document.querySelectorAll<HTMLButtonElement>("#seg-actions button")) {
  btn.onclick = async () => {
    const p = project();
    if (!p || selected < 0 || busy) return;
    const v = await attempt(() => Studio.setOverride(p.id, selected, btn.dataset.mode!));
    if (v) setView(v);
  };
}

function renderRules() {
  const p = project();
  if (!p) return;
  const chips: HTMLElement[] = p.edits.rules.map((r) => {
    const levels = r.question?.type === "score" && Array.isArray(r.question.criteria) ? r.question.criteria.length : 0;
    const sens = el("input", { type: "range", className: "sens", min: levels ? "0" : "0.1", max: levels ? String(levels - 1) : "0.9", step: levels ? "0.25" : "0.05", value: String(r.threshold) });
    sens.title = `匹配门槛 ${levels ? r.threshold.toFixed(2) + " 级" : Math.round(r.threshold * 100) + "%"}：往左更宽松，往右更严格`;
    sens.onchange = async () => {
      const v = await attempt(() => Studio.setRule(p.id, r.id, r.enabled, Number(sens.value)));
      if (v) setView(v);
    };
    const toggle = el("button", { textContent: r.enabled ? "停用" : "启用" });
    toggle.onclick = async () => {
      const v = await attempt(() => Studio.setRule(p.id, r.id, !r.enabled, r.threshold));
      if (v) setView(v);
    };
    const del = el("button", { textContent: "✕", title: "删除这条要求" });
    del.onclick = async () => {
      const v = await attempt(() => Studio.deleteRule(p.id, r.id));
      if (v) setView(v);
    };
    return el(
      "span",
      { class: "rule" + (r.enabled ? "" : " off"), title: r.text },
      el("span", { class: `tag ${r.mode}`, textContent: MODE_LABELS[r.mode] ?? r.mode }),
      el("span", { class: "text", textContent: r.text }),
      sens,
      toggle,
      del,
    );
  });
  p.edits.ranges.forEach((r, i) => {
    const del = el("button", { textContent: "✕" });
    del.onclick = async () => {
      const v = await attempt(() => Studio.deleteRange(p.id, i));
      if (v) setView(v);
    };
    chips.push(el("span", { class: "rule" }, el("span", { class: `tag ${r.keep ? "keep" : "drop"}`, textContent: r.keep ? "保留" : "去掉" }), el("span", { class: "text", textContent: `${clock(r.start)} – ${clock(r.end)}` }), del));
  });
  $("#rules").replaceChildren(...chips);
  $("#interest-opt").hidden = p.edits.rules.some((r) => r.enabled && r.mode === "keep");
  $<HTMLButtonElement>("#analyze").textContent = view!.summary.analyzed < view!.summary.segments ? "智能剪辑" : "重新挑选";
}

function renderOptions() {
  const p = project();
  if (!p) return;
  $<HTMLInputElement>("#interest").value = String(p.edits.interestThreshold);
  $("#interest-v").textContent = p.edits.interestThreshold.toFixed(1);
  $<HTMLInputElement>("#fill").checked = p.edits.fillGaps;
  $<HTMLSelectElement>("#pad").value = String(p.edits.padSeconds ?? 0);
  if (document.activeElement !== $("#target")) {
    $<HTMLInputElement>("#target").value = p.edits.targetSeconds ? String(Math.round(p.edits.targetSeconds)) : "";
  }
}

let optTimer = 0;
function onOptions() {
  const p = project();
  if (!p || busy) return;
  const interest = Number($<HTMLInputElement>("#interest").value);
  $("#interest-v").textContent = interest.toFixed(1);
  clearTimeout(optTimer);
  optTimer = window.setTimeout(async () => {
    const v = await attempt(() =>
      Studio.setOptions(p.id, interest, Number($<HTMLInputElement>("#target").value) || 0, $<HTMLInputElement>("#fill").checked, Number($<HTMLSelectElement>("#pad").value)),
    );
    if (v) setView(v);
  }, 150);
}
$("#interest").addEventListener("input", onOptions);
$("#target").addEventListener("change", onOptions);
$("#fill").addEventListener("input", onOptions);
$("#pad").addEventListener("change", onOptions);

function renderMessages(lastPending = "") {
  const p = project();
  if (!p) return;
  const box = $("#messages");
  const els = (p.chat ?? []).map((m) => el("div", { class: `msg ${m.role}`, textContent: m.text }));
  if (lastPending) els.push(el("div", { class: "msg assistant pending", textContent: lastPending }));
  box.replaceChildren(...els);
  box.scrollTop = box.scrollHeight;
}

function renderChips() {
  const p = project();
  if (!p) return;
  const fromTemplate = examplesFor(p);
  const ideas = fromTemplate.length
    ? [...fromTemplate, "导出"]
    : p.edits.rules.length
      ? ["再短一点", "撤销", "导出"]
      : ["保留最精彩的部分", "去掉片头和片尾", "控制在 3 分钟以内"];
  $("#chips").replaceChildren(
    ...ideas.map((t) => {
      const b = el("button", { type: "button", textContent: t });
      b.onclick = () => send(t);
      return b;
    }),
  );
}

async function renderModelChip() {
  const name = await attempt(() => Studio.chatModelName());
  const chip = $("#model-chip");
  chip.textContent = name ?? "";
  chip.title = `当前聊天模型：${name}。点击切换。`;
}
$("#model-chip").onclick = () => openSettings("models");

function renderBusy() {
  const on = busy !== "";
  for (const id of ["#undo", "#analyze", "#tpl-apply", "#tpl-save"]) {
    const b = document.querySelector<HTMLButtonElement>(id);
    if (b) b.disabled = on;
  }
  // Export lights up only once there is a cut to export.
  const exp = document.querySelector<HTMLButtonElement>("#export");
  if (exp) {
    const ready = !!view?.ranges.length;
    exp.classList.toggle("primary", ready);
    exp.disabled = on || !ready;
    exp.title = ready ? "导出成片（⌘E）" : "还没有成片：先在聊天里说要求，或用剪辑要求 → 智能剪辑";
  }
  const send = $<HTMLButtonElement>("#send");
  send.disabled = on;
  $("#progress").hidden = !on;
  if (!on) setProgress(0, 0, "");
}

function setProgress(done: number, total: number, label: string) {
  $<HTMLDivElement>("#progress .bar div").style.width = total ? `${(done / total) * 100}%` : busy ? "100%" : "0";
  $("#progress-label").textContent = label;
}

// ---- long operations ----

let frame = 0;
function onEvent(e: Event, started: number) {
  const p = project();
  if (e.kind === "status" && e.text) {
    pendingText = e.text;
    renderMessages(pendingText);
    setProgress(0, 0, e.text.split("\n").pop()!);
  }
  if (e.kind === "analyze" && p) {
    if (e.segment) p.segments[e.segment.index] = { ...e.segment, kept: p.segments[e.segment.index]?.kept ?? false };
    const secs = (performance.now() - started) / 1000;
    const eta = e.done ? Math.round((secs / e.done) * (e.total - e.done)) : 0;
    setProgress(e.done, e.total, `分析 ${e.done}/${e.total} 段` + (eta && e.done < e.total ? ` · 还需约 ${clock(eta)}` : ""));
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(renderTimeline);
  }
  if (e.kind === "export") {
    const label = e.text === "剪辑" || !e.text ? `剪辑 ${e.done}/${e.total} 个片段` : `${e.text} ${Math.round((e.done / Math.max(1, e.total)) * 100)}%`;
    setProgress(e.done, e.total, label);
  }
}

async function run<T>(kind: typeof busy, fn: (ch: Channel<Event>) => Promise<T>): Promise<T | undefined> {
  if (busy) {
    toast("请等当前操作完成");
    return undefined;
  }
  busy = kind;
  const started = performance.now();
  running = new Channel<Event>((e) => onEvent(e, started));
  renderBusy();
  setProgress(0, 0, kind === "export" ? "准备导出…" : "处理中…");
  try {
    return await fn(running);
  } catch (err) {
    toast(err instanceof Error ? err.message : String(err), true);
    return undefined;
  } finally {
    busy = "";
    running = null;
    pendingText = "";
    renderBusy();
  }
}
$("#stop").onclick = () => running?.close();

async function send(text: string) {
  const p = project();
  text = text.trim();
  if (!p || !text) return;
  if (busy) return toast("请等当前操作完成");
  $<HTMLTextAreaElement>("#input").value = "";
  p.chat = [...(p.chat ?? []), { role: "user", text, at: new Date().toISOString() }];
  pendingText = "…";
  renderMessages(pendingText);
  const res = await run("chat", (ch) => Studio.chat(p.id, text, ch));
  if (res?.view) setView(res.view);
  else setView(await attempt(() => Studio.open(p.id)));
  if (res?.export) exportVideo();
}
$("#composer").addEventListener("submit", (e) => {
  e.preventDefault();
  send($<HTMLTextAreaElement>("#input").value);
});
$("#input").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    send($<HTMLTextAreaElement>("#input").value);
  }
});
$("#send").innerHTML = icons.send;

$("#analyze").onclick = async () => {
  const p = project();
  if (!p) return;
  $("#rules-pop").hidden = true;
  const v = await run("analyze", (ch) => Studio.analyze(p.id, ch));
  if (v) {
    setView(v);
    toast(`已挑出 ${v.summary.clips} 个片段，成片 ${clock(v.summary.keptSeconds)}`);
  } else setView(await attempt(() => Studio.open(p.id)));
};

async function undo() {
  const p = project();
  if (!p || busy) return;
  const v = await attempt(() => Studio.undo(p.id));
  if (v) setView(v);
}

// ---- export ----

const fxChecks = { intro: "#fx-intro", transitions: "#fx-transitions", highlights: "#fx-highlights", ending: "#fx-ending", outro: "#fx-outro", progressBar: "#fx-progress" } as const;

async function askEffects(id: string): Promise<EffectSettings | null> {
  const fx = await attempt(() => Studio.effectsFor(id));
  if (!fx) return null;
  $<HTMLSelectElement>("#fx-style").value = fx.style || "none";
  $<HTMLInputElement>("#fx-title").value = fx.title;
  $<HTMLInputElement>("#fx-subtitle").value = fx.subtitle;
  $<HTMLInputElement>("#fx-accent").value = fx.accent || "#7c5cff";
  for (const [k, sel] of Object.entries(fxChecks)) $<HTMLInputElement>(sel).checked = fx[k as keyof typeof fxChecks];
  const sync = () => ($("#fx-options").hidden = $<HTMLSelectElement>("#fx-style").value === "none");
  $("#fx-style").onchange = sync;
  sync();
  const d = $<HTMLDialogElement>("#export-dialog");
  d.returnValue = "";
  d.showModal();
  await new Promise((resolve) => d.addEventListener("close", resolve, { once: true }));
  if (d.returnValue !== "export") return null;
  const out: EffectSettings = {
    style: $<HTMLSelectElement>("#fx-style").value,
    title: $<HTMLInputElement>("#fx-title").value.trim(),
    subtitle: $<HTMLInputElement>("#fx-subtitle").value.trim(),
    accent: $<HTMLInputElement>("#fx-accent").value,
    intro: false, transitions: false, highlights: false, ending: false, outro: false, progressBar: false,
  };
  for (const [k, sel] of Object.entries(fxChecks)) out[k as keyof typeof fxChecks] = $<HTMLInputElement>(sel).checked;
  return out;
}

async function exportVideo() {
  const p = project();
  if (!p) return;
  if (busy) return toast("请等当前操作完成");
  if (!view?.ranges.length) return toast("还没有要保留的片段", true);
  const fx = await askEffects(p.id);
  if (!fx) return;
  const r = await run("export", (ch) => Studio.export(p.id, fx, ch));
  if (!r) return;
  lastExport = r;
  $<HTMLVideoElement>("#out-video").src = r.mediaUrl;
  $("#out-meta").textContent = `${clock(r.duration)} · ${mb(r.size)} · ${r.path}`;
  $<HTMLDialogElement>("#result-dialog").showModal();
  setView(await attempt(() => Studio.open(p.id)));
}
$("#reveal").onclick = () => lastExport && Studio.reveal(lastExport.path);
$("#result-close").onclick = () => {
  $<HTMLVideoElement>("#out-video").pause();
  $<HTMLDialogElement>("#result-dialog").close();
};

// ---- preview: play only the kept ranges ----

const video = $<HTMLVideoElement>("#video");
video.addEventListener("timeupdate", () => {
  const p = project();
  if (!p) return;
  setPlayhead(video.currentTime, !video.paused);
  if (playUntil !== null && video.currentTime >= playUntil) {
    video.pause();
    playUntil = null;
  }
  if (!$<HTMLInputElement>("#preview").checked || video.paused || !view) return;
  const t = video.currentTime;
  const ranges: Range[] = view.ranges;
  if (ranges.some((r) => t >= r.start && t < r.end - 0.05)) return;
  const next = ranges.find((r) => r.start > t);
  if (next) video.currentTime = next.start;
  else video.pause();
});
$("#preview").addEventListener("change", () => {
  const first = view?.ranges[0];
  if ($<HTMLInputElement>("#preview").checked && first) {
    video.currentTime = first.start;
    video.play();
  }
});

// ---- timeline selection ----

/** zoomStep returns the pixels per second after zooming by factor. */
function zoomStep(factor: number) {
  const w = $("#tl-content").getBoundingClientRect().width;
  const d = project()?.source.duration ?? 1;
  return (w / d) * factor;
}

let playUntil: number | null = null;
const fmtSec = (t: number) => `${clock(Math.floor(t))}.${Math.floor((t % 1) * 10)}`;

initTimeline({
  duration: () => project()?.source.duration ?? 0,
  seek: (t) => (video.currentTime = t),
  onSelect: (sel: Selection | null) => {
    $("#sel-actions").hidden = !sel;
    $("#detail").hidden = !!sel;
    if (sel) $("#sel-label").textContent = `选区 ${fmtSec(sel.start)} – ${fmtSec(sel.end)}（${(sel.end - sel.start).toFixed(1)} 秒）`;
  },
});

for (const b of document.querySelectorAll<HTMLButtonElement>("#sel-actions button")) {
  b.onclick = async () => {
    const sel = getSelection();
    const p = project();
    if (!sel || !p) return;
    switch (b.dataset.sel) {
      case "play":
        video.currentTime = sel.start;
        playUntil = sel.end;
        video.play();
        return;
      case "clear":
        clearSelection();
        return;
    }
    const v = await attempt(() => Studio.addRange(p.id, sel.start, sel.end, b.dataset.sel === "keep"));
    if (v) {
      setView(v);
      clearSelection();
      toast(`${b.dataset.sel === "keep" ? "已保留" : "已剪掉"} ${fmtSec(sel.start)} – ${fmtSec(sel.end)}`);
    }
  };
}

// ---- keyboard ----

document.addEventListener("keydown", (e) => {
  const typing = ["TEXTAREA", "INPUT", "SELECT"].includes(document.activeElement?.tagName ?? "");
  const cmd = e.metaKey || e.ctrlKey;
  if (cmd && e.key === "z" && !typing) {
    e.preventDefault();
    undo();
  } else if (cmd && e.key === "o") {
    e.preventDefault();
    pick();
  } else if (cmd && e.key === ",") {
    e.preventDefault();
    openSettings();
  } else if (cmd && e.key === "e" && mode === "edit") {
    e.preventDefault();
    exportVideo();
  } else if (cmd && (e.key === "=" || e.key === "+") && mode === "edit") {
    e.preventDefault();
    setZoom(zoomStep(1.6));
  } else if (cmd && e.key === "-" && mode === "edit") {
    e.preventDefault();
    setZoom(zoomStep(1 / 1.6));
  } else if (cmd && e.key === "0" && mode === "edit") {
    e.preventDefault();
    fit();
  } else if (e.key === "Escape") {
    $("#rules-pop").hidden = true;
    clearSelection();
  } else if (e.key === " " && !typing && mode === "edit" && project()) {
    e.preventDefault();
    video.paused ? video.play() : video.pause();
  }
});

// ---- dropped files ----

try {
  onFileDrop(async ({ paths }) => {
    const videos = paths.filter((p) => VIDEO_EXT.test(p));
    const subs = paths.filter((p) => SUB_EXT.test(p));
    if (subs.length || mode === "subs") {
      setMode("subs");
      subsDrop(paths);
      return;
    }
    if (videos[0]) openView(await attempt(() => Studio.openPath(videos[0]!)));
  });
} catch {
  /* not running inside the app */
}

// ---- connection status ----

async function renderConnection() {
  const ms = await attempt(() => Studio.getModelSettings());
  const line = $("#home-status");
  if (!ms) return;
  line.textContent = ms.cloudflare.connected ? `Cloudflare 已连接（${ms.cloudflare.using}）` : `Cloudflare 未连接：${ms.cloudflare.using}`;
  line.classList.toggle("bad", !ms.cloudflare.connected);
}

// ---- automation hooks (used by the self-test) ----

declare global {
  interface Window {
    flashcut: {
      projectId(): string;
      busy(): boolean;
      send(text: string): void;
      lastReply(): string;
      selectFirstKept(): void;
      setMode(m: "edit" | "subs"): void;
      openRules(): void;
      openSettings(): void;
      subs(video: string, sub: string): Promise<void>;
      subsBusy(): boolean;
      selectIssue(): void;
      zoom(pps: number, start: number, end: number): void;
    };
  }
}
window.flashcut = {
  projectId: () => project()?.id ?? "",
  busy: () => busy !== "",
  send: (text) => void send(text),
  lastReply: () => [...(project()?.chat ?? [])].reverse().find((m) => m.role === "assistant")?.text ?? "",
  selectFirstKept: () => {
    const s = project()?.segments.find((s) => s.kept);
    if (s) selectSegment(s.index, true);
  },
  setMode: (m) => setMode(m),
  openRules: () => {
    const b = document.querySelector<HTMLElement>("#rules-btn");
    if (b) toggleRules(b);
  },
  openSettings: () => void openSettings(),
  subs: async (video, sub) => {
    setMode("subs");
    await subsDrop([video, sub]);
    $<HTMLButtonElement>("#subs-run").click();
  },
  subsBusy: () => !$("#subs-progress").hidden,
  zoom: (pps, start, end) => {
    setZoom(pps, start, $("#tl-scroll").getBoundingClientRect().left + 100);
    selectRange({ start, end });
    video.currentTime = start;
  },
  selectIssue: () => {
    const row = [...document.querySelectorAll<HTMLElement>("#cues .row-item")].find((r) => r.querySelector(".dot.fixed"));
    row?.click();
  },
};

// ---- start ----

initLayout();
initSettings(() => {
  renderModelChip();
  renderConnection();
});
initSubs();
await initTemplates({ project, setView, run });
renderModelChip();
const initial = await attempt(() => Studio.initial());
if (initial) openView(initial);
else showHome();
