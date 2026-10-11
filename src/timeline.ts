// Zoomable timeline: a ruler, pinch / ⌘-scroll zoom around the pointer,
// scrubbing, and drag-to-select an exact time range.
import { $ } from "./ui";

export type Selection = { start: number; end: number };

type Hooks = {
  duration(): number;
  seek(t: number): void;
  onSelect(sel: Selection | null): void;
  /** Called after the zoom changes so bars can be redrawn if needed. */
  onZoom?(): void;
};

const MAX_PPS = 60; // pixels per second at the deepest zoom
let pps = 0; // current pixels per second; 0 until the first fit
let hooks: Hooks;
let selection: Selection | null = null;
let suppressClick = false;

const scroller = () => $("#tl-scroll");
const content = () => $("#tl-content");
const viewWidth = () => Math.max(100, scroller().clientWidth - 24); // minus padding

function fitPps() {
  const d = hooks.duration();
  return d > 0 ? viewWidth() / d : 1;
}

/** zoomLevel is how many times wider than "fit" the timeline is. */
export function zoomLevel() {
  return pps / fitPps();
}

function sliderToPps(v: number) {
  const lo = Math.log(fitPps());
  const hi = Math.log(Math.max(fitPps() * 1.01, MAX_PPS));
  return Math.exp(lo + (hi - lo) * (v / 1000));
}

function ppsToSlider(p: number) {
  const lo = Math.log(fitPps());
  const hi = Math.log(Math.max(fitPps() * 1.01, MAX_PPS));
  return Math.round(((Math.log(p) - lo) / (hi - lo)) * 1000);
}

/** setZoom sets pixels per second, keeping time `anchor` under screen x `anchorX`. */
export function setZoom(next: number, anchor?: number, anchorX?: number) {
  const d = hooks.duration();
  if (!d) return;
  const fit = fitPps();
  next = Math.min(Math.max(next, fit), Math.max(fit, MAX_PPS));
  const sc = scroller();
  const rect = sc.getBoundingClientRect();
  const x = anchorX ?? rect.left + rect.width / 2;
  const t = anchor ?? timeAtClientX(x);
  pps = next;
  content().style.width = `${Math.round(d * pps)}px`;
  sc.scrollLeft = Math.max(0, t * pps - (x - rect.left - 12));
  $<HTMLInputElement>("#tl-zoom").value = String(ppsToSlider(pps));
  const level = pps / fit;
  $("#tl-zoom-v").textContent = level < 1.05 ? "1×" : `${level < 10 ? level.toFixed(1) : Math.round(level)}× · ${secondsPerPixelLabel()}`;
  renderRuler();
  renderSelection();
  hooks.onZoom?.();
}

function secondsPerPixelLabel() {
  const span = viewWidth() / pps;
  return span >= 60 ? `可见 ${Math.round(span / 60)} 分钟` : `可见 ${Math.round(span)} 秒`;
}

let fitted = 0;

export function fit() {
  setZoom(fitPps(), 0, scroller().getBoundingClientRect().left + 12);
  fitted = pps;
}

/** timeAtClientX converts a window x to seconds on the timeline. */
export function timeAtClientX(x: number) {
  const r = content().getBoundingClientRect();
  return Math.min(hooks.duration(), Math.max(0, (x - r.left) / Math.max(pps, 1e-6)));
}

export function xOf(t: number) {
  return t * pps;
}

const STEPS = [0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600];

function fmt(t: number, step: number) {
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const s = t % 60;
  const ss = step < 1 ? s.toFixed(1).padStart(4, "0") : String(Math.floor(s)).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

function renderRuler() {
  const d = hooks.duration();
  const ruler = $("#ruler");
  if (!d || !pps) return;
  const step = STEPS.find((s) => s * pps >= 70) ?? 3600;
  const minor = step / 5;
  const parts: string[] = [];
  for (let t = 0; t <= d + 1e-6; t += minor) {
    const major = Math.abs(t / step - Math.round(t / step)) < 1e-6;
    parts.push(`<i class="tick${major ? " major" : ""}" style="left:${(t * pps).toFixed(1)}px"></i>`);
    if (major) parts.push(`<span class="label" style="left:${(t * pps).toFixed(1)}px">${fmt(t, step)}</span>`);
  }
  ruler.innerHTML = parts.join("");
}

export function setPlayhead(t: number, follow: boolean) {
  $("#playhead").style.left = `${t * pps}px`;
  if (!follow) return;
  // Keep the playhead in view while playing, paging like an editor does.
  const sc = scroller();
  const x = t * pps;
  if (x < sc.scrollLeft || x > sc.scrollLeft + viewWidth() - 20) sc.scrollLeft = Math.max(0, x - viewWidth() * 0.1);
}

function renderSelection() {
  const box = $("#tl-selection");
  if (!selection || !pps) {
    box.hidden = true;
    return;
  }
  box.hidden = false;
  box.style.left = `${selection.start * pps}px`;
  box.style.width = `${Math.max(1, (selection.end - selection.start) * pps)}px`;
}

export function clearSelection() {
  selection = null;
  renderSelection();
  hooks.onSelect(null);
}

/** selectRange selects a span programmatically and brings it into view. */
export function selectRange(sel: Selection) {
  selection = sel;
  renderSelection();
  hooks.onSelect(sel);
  scroller().scrollLeft = Math.max(0, sel.start * pps - 80);
}

export function getSelection() {
  return selection;
}

/** consumeClick reports whether a click just ended a drag (and resets it). */
export function consumeClick() {
  const s = suppressClick;
  suppressClick = false;
  return s;
}

export function initTimeline(h: Hooks) {
  hooks = h;
  const sc = scroller();

  // ⌘/Ctrl + wheel and trackpad pinch zoom around the pointer; a plain
  // vertical wheel scrolls sideways when zoomed in.
  sc.addEventListener(
    "wheel",
    (e) => {
      if (!hooks.duration()) return;
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault();
        const t = timeAtClientX(e.clientX);
        setZoom(pps * Math.exp(-e.deltaY * 0.01), t, e.clientX);
      } else if (Math.abs(e.deltaY) > Math.abs(e.deltaX) && sc.scrollWidth > sc.clientWidth) {
        e.preventDefault();
        sc.scrollLeft += e.deltaY;
      }
    },
    { passive: false },
  );

  $("#tl-zoom").addEventListener("input", (e) => setZoom(sliderToPps(Number((e.target as HTMLInputElement).value))));
  $("#tl-zoom-in").onclick = () => setZoom(pps * 1.6);
  $("#tl-zoom-out").onclick = () => setZoom(pps / 1.6);
  $("#tl-fit").onclick = fit;

  // Scrub on the ruler.
  const ruler = $("#ruler");
  ruler.addEventListener("pointerdown", (e) => {
    ruler.setPointerCapture(e.pointerId);
    hooks.seek(timeAtClientX(e.clientX));
    const move = (ev: PointerEvent) => hooks.seek(timeAtClientX(ev.clientX));
    const up = () => {
      ruler.removeEventListener("pointermove", move);
      ruler.removeEventListener("pointerup", up);
    };
    ruler.addEventListener("pointermove", move);
    ruler.addEventListener("pointerup", up);
  });

  // Drag on the bars to select a range; a short press stays a click.
  const tl = $("#timeline");
  tl.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    const x0 = e.clientX;
    const t0 = timeAtClientX(x0);
    let dragging = false;
    const move = (ev: PointerEvent) => {
      if (!dragging && Math.abs(ev.clientX - x0) < 4) return;
      if (!dragging) {
        dragging = true;
        tl.setPointerCapture(e.pointerId);
      }
      const t1 = timeAtClientX(ev.clientX);
      selection = { start: Math.min(t0, t1), end: Math.max(t0, t1) };
      renderSelection();
      hooks.onSelect(selection);
      // Scroll when dragging past the edges.
      const r = scroller().getBoundingClientRect();
      if (ev.clientX > r.right - 30) scroller().scrollLeft += 20;
      if (ev.clientX < r.left + 30) scroller().scrollLeft -= 20;
    };
    const up = () => {
      tl.removeEventListener("pointermove", move);
      tl.removeEventListener("pointerup", up);
      if (dragging) {
        suppressClick = true;
        if (selection && selection.end - selection.start < 0.2) clearSelection();
      }
    };
    tl.addEventListener("pointermove", move);
    tl.addEventListener("pointerup", up);
  });

  // Keep "fit" fitted when the panel resizes; otherwise keep the zoom.
  new ResizeObserver(() => {
    if (!hooks.duration()) return;
    if (!pps || pps <= fitted * 1.01) fit();
    else setZoom(pps);
  }).observe(sc);
}

/** reset fits the timeline to a newly opened video. */
export function resetTimeline() {
  selection = null;
  renderSelection();
  requestAnimationFrame(fit);
}
