import { Channel } from "mygo-runtime";
import { Studio, type Item, type SubJobView, type SubProgress } from "./mygo";
import { icons } from "./icons";
import { $, attempt, clock, el, message, toast } from "./ui";

const STATUS: Record<string, string> = {
  pending: "待校对",
  ok: "正确",
  fixed: "已修正",
  suspect: "需确认",
  no_speech: "没人说话",
  error: "出错",
};

let videoPath = "";
let subPath = "";
let current: SubJobView | null = null;
let selected = -1;
let running: Channel<SubProgress> | null = null;

const job = () => current?.job ?? null;
const items = (): Item[] => (job()?.items ?? []).filter((x): x is Item => !!x);

export function initSubs() {
  $("#subs-video").onclick = async () => {
    const p = await attempt(() => Studio.subsPick("video"));
    if (p) setVideo(p);
  };
  $("#subs-file").onclick = async () => {
    const p = await attempt(() => Studio.subsPick("subtitle"));
    if (p) setSub(p);
  };
  $("#subs-source").onchange = () => prepare();
  $("#subs-run").onclick = runCheck;
  $("#subs-stop").onclick = () => running?.close();
  $("#subs-filter").onchange = renderList;
  const v = $<HTMLVideoElement>("#subs-video-el");
  v.addEventListener("timeupdate", renderOverlay);
}

/** subsToolbar returns the title bar buttons for subtitle mode. */
export function subsToolbar(): HTMLElement[] {
  const recheck = el("button", { title: "清除结果，全部重新校对", textContent: "重新校对" });
  recheck.disabled = !job() || !!running;
  recheck.onclick = async () => {
    if (!job()) return;
    const v = await attempt(() => Studio.subsRecheck(job()!.id));
    if (v) show(v);
  };
  const exp = el("button", { class: "primary", title: "导出修正后的字幕文件" });
  exp.innerHTML = `${icons.export}<span style="margin-left:4px">导出字幕</span>`;
  exp.style.display = "inline-flex";
  exp.style.alignItems = "center";
  exp.disabled = !job();
  exp.onclick = async () => {
    if (!job()) return;
    const out = await attempt(() => Studio.subsExport(job()!.id));
    if (out) toast(`已导出到 ${out}`);
  };
  return [recheck, exp];
}

/** subsHome refreshes the subtitle workbench when the mode opens. */
export function subsHome() {
  renderAll();
}

/** subsDrop takes dropped files, or opens a saved check by id. */
export async function subsDrop(paths: string[], id?: string) {
  if (id) {
    const v = await attempt(() => Studio.subsOpen(id));
    if (v?.job) {
      videoPath = v.job.video.path;
      subPath = v.job.subPath;
      ($("#subs-source") as HTMLSelectElement).value = v.job.setting || "auto";
      show(v);
    }
    return;
  }
  for (const p of paths) {
    if (/\.(srt|vtt|ass|ssa)$/i.test(p)) subPath = p;
    else videoPath = p;
  }
  renderNames();
  await prepare();
}

function setVideo(p: string) {
  videoPath = p;
  renderNames();
  prepare();
}

function setSub(p: string) {
  subPath = p;
  renderNames();
  prepare();
}

function renderNames() {
  $("#subs-video-name").textContent = videoPath ? videoPath.split("/").pop()! : "未选择视频";
  $("#subs-video-name").title = videoPath;
  $("#subs-file-name").textContent = subPath ? subPath.split("/").pop()! : "未选择字幕";
  $("#subs-file-name").title = subPath;
}

async function prepare() {
  if (!videoPath || !subPath) {
    renderAll();
    return;
  }
  const v = await attempt(() => Studio.subsCreate(videoPath, subPath, $<HTMLSelectElement>("#subs-source").value));
  if (v) show(v);
}

function show(v: SubJobView) {
  const prev = current?.job?.video.path;
  current = v;
  if (v.job && v.job.video.path !== prev) $<HTMLVideoElement>("#subs-video-el").src = v.mediaUrl;
  renderAll();
}

function renderAll() {
  renderNames();
  renderList();
  renderSummary();
  renderDetail();
  const tb = document.querySelector("#toolbar");
  if (tb && !$("#subs").hidden) {
    // Refresh the export button state.
    const [recheck, exp] = subsToolbar();
    const old = tb.querySelectorAll("button");
    if (old[0] && old[1]) {
      old[0].replaceWith(recheck!);
      old[1].replaceWith(exp!);
    }
  }
  $<HTMLButtonElement>("#subs-run").disabled = !job() || !!running;
  const pending = items().filter((i) => i.status === "pending" || i.status === "error").length;
  $("#subs-run").textContent = !job() ? "开始校对" : pending === items().length ? "开始校对" : pending ? `继续校对（${pending}）` : "已校对完";
}

function renderSummary() {
  const j = job();
  if (!j) {
    $("#subs-summary").textContent = "选择视频和字幕文件（或拖进窗口）后开始校对。";
    return;
  }
  const c = current!.counts ?? {};
  const src = j.source === "burned" ? "对照画面字幕" : j.source === "audio" ? "对照语音" : "";
  const parts = [`${items().length} 条字幕`];
  for (const k of ["fixed", "suspect", "no_speech", "error", "ok", "pending"]) if (c[k]) parts.push(`${STATUS[k]} ${c[k]}`);
  if (src) parts.push(src);
  if (j.tokens) parts.push(`已花费 ≈ $${current!.costUsd.toFixed(3)}`);
  $("#subs-summary").textContent = parts.join(" · ");
}

function visible(it: Item) {
  switch ($<HTMLSelectElement>("#subs-filter").value) {
    case "issues":
      return it.status !== "ok" && it.status !== "pending";
    case "fixed":
      return it.status === "fixed";
    case "suspect":
      return it.status === "suspect" || it.status === "no_speech" || it.status === "error";
  }
  return true;
}

function renderList() {
  const list = items().filter(visible);
  if (!job()) {
    $("#cues").replaceChildren(el("div", { class: "empty-note", textContent: "还没有字幕。" }));
    return;
  }
  $("#cues").replaceChildren(
    ...list.map((it) => {
      const text = it.status === "fixed" && it.accepted ? it.suggestion! : it.text;
      const row = el(
        "div",
        { class: "row-item" + (it.index === selected ? " active" : "") },
        el("div", { class: "time" }, el("span", { class: `dot ${it.status}`, title: STATUS[it.status] }), `#${it.index}  ${clock(it.start)}`, it.status !== "ok" && it.status !== "pending" ? (STATUS[it.status] ?? "") : ""),
        el("div", { class: "text", textContent: text }),
      );
      row.onclick = () => select(it.index, true);
      return row;
    }),
  );
}

function select(index: number, seek: boolean) {
  selected = index;
  const it = items().find((x) => x.index === index);
  if (seek && it) $<HTMLVideoElement>("#subs-video-el").currentTime = it.start + 0.05;
  renderList();
  renderDetail();
}

function box(label: string, text: string) {
  return el("div", {}, el("div", { class: "label", textContent: label }), el("div", { class: "box", textContent: text || "—" }));
}

function renderDetail() {
  const it = items().find((x) => x.index === selected);
  const d = $("#cue-detail");
  if (!it) {
    d.replaceChildren(el("p", { class: "muted", textContent: job() ? "选择左侧的一条字幕查看判断和修改建议。" : "选择视频和字幕文件后开始校对。" }));
    return;
  }
  const j = job()!;
  const parts: HTMLElement[] = [
    el("div", { class: "status-pill" }, el("span", { class: `dot ${it.status}` }), STATUS[it.status] ?? String(it.status), el("span", { class: "muted", textContent: `#${it.index} · ${clock(it.start)} – ${clock(it.end)}` })),
  ];
  if (it.status !== "pending") {
    parts.push(el("div", { class: "muted", textContent: `Clef-omni 判断字幕正确的概率 ${Math.round(it.match * 100)}%` + (j.source === "audio" ? ` · 有人说话的概率 ${Math.round(it.speech * 100)}%` : "") }));
  }
  parts.push(box("原字幕", it.text));
  if (it.evidence) parts.push(box(j.source === "burned" ? "画面上的字" : "听到的内容（语音识别）", it.evidence));
  const ta = el("textarea", { value: it.suggestion || it.text });
  const accept = el("input", { type: "checkbox", checked: it.accepted });
  const save = el("button", { class: "primary small", textContent: "保存" });
  save.onclick = async () => {
    const v = await attempt(() => Studio.subsEdit(j.id, it.index, accept.checked || ta.value.trim() !== it.text, ta.value));
    if (v) {
      show(v);
      toast("已保存");
    }
  };
  parts.push(
    el("div", {}, el("div", { class: "label", textContent: it.suggestion ? "修改建议（可以直接改）" : "修改（可以直接改）" }), ta),
    el("div", { class: "actions" }, el("label", { class: "check" }, accept, "导出时采用"), save),
  );
  if (it.reason) parts.push(box("说明", it.reason));
  if (it.error) parts.push(box("错误", it.error));
  d.replaceChildren(...parts);
}

function renderOverlay() {
  const v = $<HTMLVideoElement>("#subs-video-el");
  const t = v.currentTime;
  const it = items().find((x) => t >= x.start && t < x.end);
  const o = $("#subs-overlay");
  if (!it || it.status === "ok" || it.status === "pending" || !it.suggestion) {
    o.hidden = true;
    return;
  }
  o.hidden = false;
  o.replaceChildren(el("div", { class: "old", textContent: it.text }), el("div", { class: "new", textContent: it.suggestion }));
}

async function runCheck() {
  const j = job();
  if (!j || running) return;
  running = new Channel<SubProgress>((p) => {
    const label = p.stage || `已校对 ${p.done}/${p.total} 条`;
    $("#subs-progress-label").textContent = label;
    $<HTMLDivElement>("#subs-progress .bar div").style.width = p.total ? `${(p.done / p.total) * 100}%` : "100%";
    if (p.item && current?.job) {
      const list = current.job.items ?? [];
      const i = list.findIndex((x) => x?.index === p.item!.index);
      if (i >= 0) list[i] = p.item;
      current.counts = {};
      for (const x of list) if (x) current.counts[x.status] = (current.counts[x.status] ?? 0) + 1;
      renderList();
      renderSummary();
      if (p.item.index === selected) renderDetail();
    }
  });
  $("#subs-progress").hidden = false;
  renderAll();
  try {
    const v = await Studio.subsRun(j.id, running);
    if (v) {
      show(v);
      const c = v.counts ?? {};
      toast(`校对完成：修正 ${c.fixed ?? 0} 条，需确认 ${(c.suspect ?? 0) + (c.no_speech ?? 0)} 条`);
      if ((c.fixed ?? 0) + (c.suspect ?? 0) > 0) $<HTMLSelectElement>("#subs-filter").value = "issues";
      renderList();
    }
  } catch (err) {
    toast(message(err), true);
  } finally {
    running = null;
    $("#subs-progress").hidden = true;
    renderAll();
  }
}
