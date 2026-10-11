import { Channel, isCallError } from "mygo-runtime";
import { Studio, type Event, type ExportResult, type Quality, type Range, type Segment, type View } from "./mygo";

const $ = <T extends HTMLElement = HTMLElement>(selector: string) => document.querySelector<T>(selector)!;

let view: View | null = null;
let selected = -1;
let busy: "" | "chat" | "analyze" | "export" = "";
let running: Channel<Event> | null = null;
let pendingText = "";
let lastExport: ExportResult | null = null;

// ---- helpers ----

const clock = (sec: number) => {
  const s = Math.max(0, Math.round(sec));
  const h = Math.floor(s / 3600);
  const mm = String(Math.floor((s % 3600) / 60)).padStart(h ? 2 : 1, "0");
  const ss = String(s % 60).padStart(2, "0");
  return h ? `${h}:${mm}:${ss}` : `${mm}:${ss}`;
};
const mb = (bytes: number) => `${(bytes / 1e6).toFixed(bytes > 1e8 ? 0 : 1)} MB`;
const message = (err: unknown) => (isCallError(err) || err instanceof Error ? err.message : String(err));
const project = () => view?.project ?? null;

let toastTimer = 0;
function toast(text: string, bad = false) {
  const el = $("#toast");
  el.textContent = text;
  el.classList.toggle("bad", bad);
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => (el.hidden = true), bad ? 6000 : 3000);
}

async function attempt<T>(fn: () => Promise<T>): Promise<T | undefined> {
  try {
    return await fn();
  } catch (err) {
    toast(message(err), true);
    return undefined;
  }
}

// ---- home ----

async function showHome() {
  view = null;
  $("#home").hidden = false;
  $("#workspace").hidden = true;
  $("#export").hidden = true;
  $("#undo").hidden = true;
  $("#project-name").textContent = "说一句话，剪好一条视频";
  const recent = (await attempt(() => Studio.recent())) ?? [];
  const box = $("#recent");
  box.replaceChildren();
  if (recent.length) {
    const h = document.createElement("h3");
    h.textContent = "最近的项目";
    box.append(h);
  }
  for (const r of recent) {
    const item = document.createElement("div");
    item.className = "recent-item" + (r.missing ? " missing" : "");
    const left = document.createElement("div");
    const name = document.createElement("div");
    name.className = "name";
    name.textContent = r.name;
    const meta = document.createElement("div");
    meta.className = "muted";
    meta.textContent = r.missing
      ? "原视频已移动或删除"
      : `${clock(r.duration)} → 成片 ${clock(r.keptSeconds)} · ${new Date(r.updatedAt).toLocaleString()}`;
    left.append(name, meta);
    const forget = document.createElement("button");
    forget.className = "small";
    forget.textContent = "移除";
    forget.onclick = async (e) => {
      e.stopPropagation();
      await attempt(() => Studio.forget(r.id));
      showHome();
    };
    item.append(left, forget);
    if (!r.missing) item.onclick = async () => openView(await attempt(() => Studio.open(r.id)));
    box.append(item);
  }
  renderConnection();
}

async function pick() {
  if (busy) return toast("请等当前操作完成");
  const v = await attempt(() => Studio.pickVideo());
  if (v) openView(v);
}
$("#open").onclick = pick;
$("#open-big").onclick = pick;

function openView(v: View | null | undefined) {
  if (!v?.project) return;
  selected = -1;
  lastExport = null;
  $("#result").hidden = true;
  setView(v);
  $("#home").hidden = true;
  $("#workspace").hidden = false;
  const video = $<HTMLVideoElement>("#video");
  video.src = v.mediaUrl;
  $<HTMLInputElement>("#target").value = v.project.edits.targetSeconds ? String(Math.round(v.project.edits.targetSeconds)) : "";
  $("#input").focus();
}

// ---- rendering ----

function setView(v: View | null | undefined) {
  if (!v?.project) return;
  view = v;
  const p = v.project;
  $("#project-name").textContent = `${p.source.path.split("/").pop()} · ${clock(p.source.duration)}`;
  $("#export").hidden = false;
  $("#undo").hidden = false;
  renderSummary();
  renderTimeline();
  renderRanges();
  renderRules();
  renderOptions();
  renderMessages();
  renderChips();
  renderBusy();
  showDetail(p.segments[selected]);
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
  const tl = $("#timeline");
  const playhead = $("#playhead");
  const bars = p.segments.map((s) => {
    const el = document.createElement("div");
    el.className = "seg";
    const bar = document.createElement("i");
    const interest = s.answers?.interest;
    if (s.error) el.classList.add("failed");
    else if (interest === undefined) el.classList.add("pending");
    else bar.style.height = `${Math.max(4, (interest / 4) * 100)}%`;
    if (s.kept) el.classList.add("kept");
    if (p.edits.overrides?.[s.index] !== undefined) el.classList.add("pinned");
    if (s.index === selected) el.classList.add("selected");
    el.append(bar);
    el.onmouseenter = () => showDetail(s);
    el.onmouseleave = () => showDetail(project()?.segments[selected]);
    el.onclick = () => selectSegment(s.index, true);
    return el;
  });
  tl.replaceChildren(...bars, playhead);
}

function renderRanges() {
  const p = project();
  if (!p || !view) return;
  const bar = $("#ranges-bar");
  bar.replaceChildren(
    ...view.ranges.map((r) => {
      const el = document.createElement("span");
      el.style.left = `${(r.start / p.source.duration) * 100}%`;
      el.style.width = `${((r.end - r.start) / p.source.duration) * 100}%`;
      return el;
    }),
  );
}

function showDetail(s: Segment | undefined) {
  const el = $("#detail");
  const p = project();
  $("#seg-actions").hidden = !(s && s.index === selected);
  if (!s || !p) {
    el.textContent = "点击时间线上的一段可以跳转预览，并手动保留或剪掉。";
    return;
  }
  const parts = [`<b>${clock(s.start)} – ${clock(s.end)}</b>`, s.kept ? "<b>保留</b>" : "剪掉"];
  if (s.error) parts.push(`分析失败：${escapeHTML(s.error)}`);
  const interest = s.answers?.interest;
  if (interest !== undefined) parts.push(`精彩程度 <b>${interest.toFixed(1)}</b>/4`);
  for (const r of p.edits.rules) {
    const v = s.answers?.[r.id];
    if (v !== undefined) parts.push(`${r.mode === "drop" ? "去掉" : "保留"}“${escapeHTML(short(r.text))}” <b>${Math.round(v * 100)}%</b>`);
  }
  if (s.reason) parts.push(escapeHTML(s.reason));
  el.innerHTML = parts.join(" · ");
}

const short = (t: string) => (t.length > 18 ? t.slice(0, 17) + "…" : t);
const escapeHTML = (t: string) => t.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]!);

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
  const box = $("#rules");
  const chips: HTMLElement[] = p.edits.rules.map((r) => {
    const el = document.createElement("span");
    el.className = "rule" + (r.enabled ? "" : " off");
    el.title = r.text;
    const tag = document.createElement("span");
    tag.className = `tag ${r.mode}`;
    tag.textContent = r.mode === "drop" ? "去掉" : "保留";
    const text = document.createElement("span");
    text.className = "text";
    text.textContent = r.text;
    // Sensitivity: right is stricter (a higher probability is needed).
    const sens = document.createElement("input");
    sens.type = "range";
    sens.min = "0.1";
    sens.max = "0.9";
    sens.step = "0.05";
    sens.value = String(r.threshold);
    sens.className = "sens";
    sens.title = `匹配门槛 ${Math.round(r.threshold * 100)}%：往左更宽松，往右更严格`;
    sens.onchange = async () => {
      const v = await attempt(() => Studio.setRule(p.id, r.id, r.enabled, Number(sens.value)));
      if (v) setView(v);
    };
    const toggle = document.createElement("button");
    toggle.textContent = r.enabled ? "停用" : "启用";
    toggle.onclick = async () => {
      const v = await attempt(() => Studio.setRule(p.id, r.id, !r.enabled, r.threshold));
      if (v) setView(v);
    };
    const del = document.createElement("button");
    del.textContent = "✕";
    del.title = "删除这条要求";
    del.onclick = async () => {
      const v = await attempt(() => Studio.deleteRule(p.id, r.id));
      if (v) setView(v);
    };
    el.append(tag, text, sens, toggle, del);
    return el;
  });
  p.edits.ranges.forEach((r, i) => {
    const el = document.createElement("span");
    el.className = "rule";
    const tag = document.createElement("span");
    tag.className = `tag ${r.keep ? "keep" : "drop"}`;
    tag.textContent = r.keep ? "保留" : "去掉";
    const text = document.createElement("span");
    text.className = "text";
    text.textContent = `${clock(r.start)} – ${clock(r.end)}`;
    const del = document.createElement("button");
    del.textContent = "✕";
    del.onclick = async () => {
      const v = await attempt(() => Studio.deleteRange(p.id, i));
      if (v) setView(v);
    };
    el.append(tag, text, del);
    chips.push(el);
  });
  box.replaceChildren(...chips);
  const hasKeep = p.edits.rules.some((r) => r.enabled && r.mode === "keep");
  $("#interest-opt").hidden = hasKeep;
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
      Studio.setOptions(
        p.id,
        interest,
        Number($<HTMLInputElement>("#target").value) || 0,
        $<HTMLInputElement>("#fill").checked,
        Number($<HTMLSelectElement>("#pad").value),
      ),
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
  const els = (p.chat ?? []).map((m) => bubble(m.role, m.text));
  if (lastPending) els.push(bubble("assistant pending", lastPending));
  box.replaceChildren(...els);
  box.scrollTop = box.scrollHeight;
}

function bubble(role: string, text: string) {
  const el = document.createElement("div");
  el.className = `msg ${role}`;
  el.textContent = text;
  return el;
}

function renderChips() {
  const p = project();
  if (!p) return;
  const ideas = p.edits.rules.length
    ? ["再短一点", "撤销", "导出"]
    : ["保留最精彩的部分", "去掉片头和片尾", "控制在 3 分钟以内", "只保留有人说话的部分"];
  $("#chips").replaceChildren(
    ...ideas.map((t) => {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = t;
      b.onclick = () => send(t);
      return b;
    }),
  );
}

function renderBusy() {
  const on = busy !== "";
  for (const id of ["#open", "#undo", "#export", "#analyze"]) $<HTMLButtonElement>(id).disabled = on;
  $<HTMLButtonElement>("#composer button").disabled = on;
  $("#progress").hidden = !on;
  if (!on) setProgress(0, 0, "");
}

function setProgress(done: number, total: number, label: string) {
  $<HTMLDivElement>(".progress .bar div").style.width = total ? `${(done / total) * 100}%` : busy ? "100%" : "0";
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
  if (e.kind === "export") setProgress(e.done, e.total, `导出 ${e.done}/${e.total} 个片段`);
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
    toast(message(err), true);
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
  renderMessages("…");
  const res = await run("chat", (ch) => Studio.chat(p.id, text, ch));
  if (res?.view) setView(res.view);
  else if (view) setView(await attempt(() => Studio.open(p.id)));
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

$("#analyze").onclick = async () => {
  const p = project();
  if (!p) return;
  const v = await run("analyze", (ch) => Studio.analyze(p.id, ch));
  if (v) {
    setView(v);
    toast(`已挑出 ${v.summary.clips} 个片段，成片 ${clock(v.summary.keptSeconds)}`);
  } else setView(await attempt(() => Studio.open(p.id)));
};

$("#undo").onclick = undo;
async function undo() {
  const p = project();
  if (!p || busy) return;
  const v = await attempt(() => Studio.undo(p.id));
  if (v) setView(v);
}
document.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key === "z" && document.activeElement?.tagName !== "TEXTAREA" && document.activeElement?.tagName !== "INPUT") {
    e.preventDefault();
    undo();
  }
});

$("#export").onclick = exportVideo;
async function exportVideo() {
  const p = project();
  if (!p) return;
  if (!view?.ranges.length) return toast("还没有要保留的片段", true);
  const r = await run("export", (ch) => Studio.export(p.id, ch));
  if (!r) return;
  lastExport = r;
  $("#result").hidden = false;
  const out = $<HTMLVideoElement>("#out-video");
  out.src = r.mediaUrl;
  $("#out-meta").textContent = `${clock(r.duration)} · ${mb(r.size)} · ${r.path}`;
  $("#result").scrollIntoView({ behavior: "smooth" });
  toast("导出完成");
  setView(await attempt(() => Studio.open(p.id)));
}
$("#reveal").onclick = () => lastExport && Studio.reveal(lastExport.path);

// ---- preview: play only the kept ranges ----

const video = $<HTMLVideoElement>("#video");
video.addEventListener("timeupdate", () => {
  const p = project();
  if (!p) return;
  $("#playhead").style.left = `${(video.currentTime / p.source.duration) * 100}%`;
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

// ---- settings ----

async function renderConnection() {
  const s = await attempt(() => Studio.getSettings());
  if (!s) return;
  const text = s.connected ? `已连接 Cloudflare（${s.connection}）` : s.connection;
  for (const id of ["#connection", "#home-status"]) {
    $(id).textContent = s.ffmpeg ? s.ffmpeg : text;
    $(id).classList.toggle("bad", !s.connected || !!s.ffmpeg);
  }
  return s;
}

$("#settings-btn").onclick = async () => {
  const s = await renderConnection();
  if (!s) return;
  $<HTMLInputElement>("#s-account").value = s.accountId;
  $<HTMLInputElement>("#s-token").value = "";
  $<HTMLInputElement>("#s-token").placeholder = s.hasToken ? "已保存（留空则不修改）" : "粘贴 API Token";
  $<HTMLInputElement>("#s-clear").checked = false;
  $<HTMLSelectElement>("#s-concurrency").value = String(s.concurrency);
  $<HTMLSelectElement>("#s-quality").value = s.quality;
  $("#conn-status").textContent = s.connected ? `当前通过 ${s.connection} 连接` : s.connection;
  const ff = $("#ffmpeg-status");
  ff.textContent = s.ffmpeg || "ffmpeg 已就绪";
  ff.classList.toggle("bad", !!s.ffmpeg);
  $<HTMLDialogElement>("#settings").showModal();
};

$<HTMLDialogElement>("#settings").addEventListener("close", async () => {
  if ($<HTMLDialogElement>("#settings").returnValue !== "save") return;
  const s = await attempt(() =>
    Studio.saveSettings(
      $<HTMLInputElement>("#s-account").value,
      $<HTMLInputElement>("#s-token").value,
      $<HTMLInputElement>("#s-clear").checked,
      Number($<HTMLSelectElement>("#s-concurrency").value),
      $<HTMLSelectElement>("#s-quality").value as Quality,
    ),
  );
  $<HTMLInputElement>("#s-token").value = "";
  if (s) {
    toast(s.connected ? "设置已保存" : `设置已保存，但还连不上：${s.connection}`, !s.connected);
    renderConnection();
  }
});

// ---- automation hooks (used by the self-test) ----

declare global {
  interface Window {
    flashcut: {
      projectId(): string;
      busy(): boolean;
      send(text: string): void;
      lastReply(): string;
      selectFirstKept(): void;
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
};

// ---- start ----

const initial = await attempt(() => Studio.initial());
if (initial) openView(initial);
else showHome();
