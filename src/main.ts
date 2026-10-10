import { Channel, isCallError } from "mygo-runtime";
import { Studio, type Options, type Progress, type Project, type RenderProgress, type Segment } from "./mygo";

const $ = <T extends HTMLElement>(selector: string) => document.querySelector<T>(selector)!;

const SCENES: Record<string, string> = {
  draft: "BP 选人",
  desk: "解说台/观众/广告",
  laning: "对线发育",
  skirmish: "小规模交战",
  teamfight: "团战",
  objective: "资源争夺",
  replay: "精彩回放",
  end: "比赛结束",
};

let project: Project | null = null;
let options: Options = await Studio.defaults();
let segments: Segment[] = [];
let selected = -1;
let busy = false;
let analyzing: Channel<Progress> | null = null;

const clock = (sec: number) => {
  const s = Math.round(sec);
  const h = Math.floor(s / 3600);
  const mm = String(Math.floor((s % 3600) / 60)).padStart(h ? 2 : 1, "0");
  const ss = String(s % 60).padStart(2, "0");
  return h ? `${h}:${mm}:${ss}` : `${mm}:${ss}`;
};
const mb = (bytes: number) => `${(bytes / 1024 / 1024).toFixed(0)} MB`;

function showError(err: unknown) {
  const el = $("#error");
  el.hidden = !err;
  el.textContent = err ? (isCallError(err) || err instanceof Error ? err.message : String(err)) : "";
}

function setBusy(on: boolean) {
  busy = on;
  for (const id of ["#pick", "#analyze", "#opt-seg"]) $<HTMLButtonElement>(id).disabled = on;
  $<HTMLButtonElement>("#render").disabled = on || !project?.ranges?.length;
}

function setProgress(done: number, total: number, label: string) {
  const el = $("#progress");
  el.hidden = total === 0;
  $(".progress .bar").style.width = total ? `${(done / total) * 100}%` : "0";
  $(".progress span").textContent = label;
}

// ---- options ----

function renderOptions() {
  $<HTMLSelectElement>("#opt-seg").value = String(options.segmentSeconds);
  $<HTMLInputElement>("#opt-keep").value = String(options.keepThreshold);
  $<HTMLInputElement>("#opt-score").value = String(options.scoreThreshold);
  $<HTMLInputElement>("#opt-fill").checked = options.fillGaps;
  $("#opt-keep-v").textContent = options.keepThreshold > 1 ? "关" : options.keepThreshold.toFixed(2);
  $("#opt-score-v").textContent = options.scoreThreshold > 4 ? "关" : options.scoreThreshold.toFixed(2);
}

let reselectTimer = 0;
function onOptionsChanged() {
  options = {
    ...options,
    segmentSeconds: Number($<HTMLSelectElement>("#opt-seg").value),
    keepThreshold: Number($<HTMLInputElement>("#opt-keep").value),
    scoreThreshold: Number($<HTMLInputElement>("#opt-score").value),
    fillGaps: $<HTMLInputElement>("#opt-fill").checked,
  };
  renderOptions();
  if (!project?.segments?.length || busy) return;
  if (options.segmentSeconds !== project.options.segmentSeconds) return; // needs a new analysis
  clearTimeout(reselectTimer);
  reselectTimer = window.setTimeout(async () => {
    try {
      const p = await Studio.reselect(project!.source.path, options);
      if (p) load(p);
    } catch (err) {
      showError(err);
    }
  }, 120);
}
for (const id of ["#opt-seg", "#opt-keep", "#opt-score", "#opt-fill"]) $(id).addEventListener("input", onOptionsChanged);

// ---- project ----

function load(p: Project) {
  project = p;
  segments = p.segments ?? [];
  $("#empty").hidden = true;
  $("#workspace").hidden = false;
  const name = p.source.path.split("/").pop()!;
  $("#src-name").textContent = name;
  $("#src-meta").textContent = `${clock(p.source.duration)} · ${p.source.width}×${p.source.height} · ${mb(p.source.size)}${p.source.hasAudio ? "" : " · 无音轨"}`;
  const video = $<HTMLVideoElement>("#src-video");
  if (video.dataset.src !== p.mediaUrl) {
    video.src = p.mediaUrl;
    video.dataset.src = p.mediaUrl;
  }
  $<HTMLButtonElement>("#analyze").textContent = segments.some((s) => s.verdict) ? "重新分析" : "开始分析";
  renderTimeline();
  renderStats();
  setBusy(busy);
}

function renderStats() {
  const el = $("#stats");
  if (!project?.stats || !segments.length) {
    el.textContent = "";
    return;
  }
  const st = project.stats;
  const pct = st.totalSeconds ? Math.round((st.keptSeconds / st.totalSeconds) * 100) : 0;
  const cost = (st.tokens / 1e6) * 0.15;
  el.textContent =
    `保留 ${st.kept}/${st.segments} 段 · ${clock(st.keptSeconds)}（${pct}%）` +
    (st.failed ? ` · 失败 ${st.failed}` : "") +
    ` · ${(st.tokens / 1000).toFixed(0)}k token ≈ $${cost.toFixed(2)} · ${project.judge}`;
}

function renderTimeline() {
  const tl = $("#timeline");
  tl.replaceChildren(
    ...segments.map((s) => {
      const el = document.createElement("div");
      el.className = "seg";
      const bar = document.createElement("i");
      if (s.verdict) {
        bar.style.height = `${Math.max(3, (s.verdict.score / 4) * 100)}%`;
        if (s.kept) el.classList.add("kept");
      } else if (s.error) {
        el.classList.add("failed");
      } else {
        el.classList.add("pending");
      }
      if (s.index === selected) el.classList.add("selected");
      el.append(bar);
      el.addEventListener("mouseenter", () => showDetail(s));
      el.addEventListener("mouseleave", () => showDetail(segments[selected]));
      el.addEventListener("click", () => onSegmentClick(s));
      return el;
    }),
  );
}

function showDetail(s: Segment | undefined) {
  const el = $("#detail");
  if (!s) {
    el.textContent = "悬停或点击某一段查看模型判定。";
    return;
  }
  const range = `<b>${clock(s.start)} – ${clock(s.end)}</b>`;
  if (s.error) {
    el.innerHTML = `${range} · 判定失败：`;
    el.append(s.error);
    return;
  }
  if (!s.verdict) {
    el.innerHTML = `${range} · 等待判定`;
    return;
  }
  const v = s.verdict;
  el.innerHTML =
    `${range} · ${s.kept ? "<b>保留</b>" : "丢弃"} · 场景：<b>${SCENES[v.scene] ?? v.scene}</b>（${Math.round((v.sceneProbs[v.scene] ?? 0) * 100)}%）` +
    ` · 保留概率 <b>${Math.round(v.keep * 100)}%</b> · 精彩分 <b>${v.score.toFixed(2)}</b>/4`;
}

async function onSegmentClick(s: Segment) {
  const video = $<HTMLVideoElement>("#src-video");
  if (selected === s.index && s.verdict && project && !busy) {
    try {
      const p = await Studio.setKept(project.source.path, s.index, !s.kept, options);
      if (p) load(p);
      showDetail(segments[selected]);
    } catch (err) {
      showError(err);
    }
    return;
  }
  selected = s.index;
  video.currentTime = s.start;
  renderTimeline();
  showDetail(s);
}

// ---- actions ----

$("#pick").addEventListener("click", async () => {
  showError(null);
  try {
    const p = await Studio.pickVideo();
    if (!p) return;
    selected = -1;
    options = { ...p.options, keepThreshold: options.keepThreshold, scoreThreshold: options.scoreThreshold, fillGaps: options.fillGaps };
    renderOptions();
    $<HTMLVideoElement>("#out-video").hidden = true;
    $("#out-empty").hidden = false;
    $("#out-meta").textContent = "";
    $("#reveal").hidden = true;
    load(p);
  } catch (err) {
    showError(err);
  }
});

$("#analyze").addEventListener("click", async () => {
  if (!project) return;
  showError(null);
  setBusy(true);
  $("#cancel").hidden = false;
  const total = Math.ceil(project.source.duration / options.segmentSeconds);
  if (options.segmentSeconds !== project.options.segmentSeconds || !segments.length) {
    segments = Array.from({ length: total }, (_, i) => ({
      index: i,
      start: i * options.segmentSeconds,
      end: Math.min((i + 1) * options.segmentSeconds, project!.source.duration),
      verdict: null,
      kept: false,
    }));
  }
  renderTimeline();
  setProgress(0, total, "准备中…");
  const started = performance.now();
  analyzing = new Channel<Progress>((p) => {
    if (p.segment) {
      segments[p.segment.index] = p.segment;
      if (p.segment.verdict) p.segment.kept = p.segment.verdict.keep >= options.keepThreshold || p.segment.verdict.score >= options.scoreThreshold;
    }
    const secs = (performance.now() - started) / 1000;
    const eta = p.done ? Math.round((secs / p.done) * (p.total - p.done)) : 0;
    setProgress(p.done, p.total, `已判定 ${p.done}/${p.total} 段` + (p.done < p.total && eta ? ` · 预计还需 ${clock(eta)}` : ""));
    renderTimeline();
  });
  try {
    const p = await Studio.analyze(project.source.path, options, analyzing);
    if (p) load(p);
    setProgress(0, 0, "");
  } catch (err) {
    showError(err);
  } finally {
    analyzing = null;
    $("#cancel").hidden = true;
    setBusy(false);
  }
});

$("#cancel").addEventListener("click", () => analyzing?.close());

$("#render").addEventListener("click", async () => {
  if (!project) return;
  showError(null);
  setBusy(true);
  setProgress(0, project.ranges.length, "剪辑中…");
  const progress = new Channel<RenderProgress>((p) => setProgress(p.done, p.total, `已剪辑 ${p.done}/${p.total} 个片段`));
  try {
    const r = await Studio.render(project.source.path, progress);
    if (!r) return;
    const video = $<HTMLVideoElement>("#out-video");
    video.src = r.mediaUrl;
    video.hidden = false;
    $("#out-empty").hidden = true;
    $("#out-meta").textContent = `${clock(r.duration)} · ${mb(r.size)} · ${r.path}`;
    const reveal = $("#reveal");
    reveal.hidden = false;
    reveal.onclick = () => Studio.reveal(r.path);
  } catch (err) {
    showError(err);
  } finally {
    setProgress(0, 0, "");
    setBusy(false);
  }
});

renderOptions();

try {
  const initial = await Studio.initial();
  if (initial) {
    options = { ...options, segmentSeconds: initial.options.segmentSeconds };
    renderOptions();
    load(initial);
  }
} catch (err) {
  showError(err);
}
