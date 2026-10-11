// Resizable, hideable panels, remembered per window like an IDE.

type PanelName = "edit-left" | "edit-right" | "edit-bottom" | "subs-left" | "subs-right";

const store = {
  get<T>(key: string, fallback: T): T {
    try {
      const v = localStorage.getItem("flashcut." + key);
      return v === null ? fallback : (JSON.parse(v) as T);
    } catch {
      return fallback;
    }
  },
  set(key: string, value: unknown) {
    try {
      localStorage.setItem("flashcut." + key, JSON.stringify(value));
    } catch {
      /* storage may be unavailable */
    }
  },
};

const spec: Record<PanelName, { host: () => HTMLElement; cssVar: string; hideClass: string; min: number; max: number; def: number; axis: "x" | "y"; invert: boolean }> = {
  "edit-left": { host: () => document.querySelector("#edit")!, cssVar: "--left-w", hideClass: "no-left", min: 160, max: 480, def: 220, axis: "x", invert: false },
  "edit-right": { host: () => document.querySelector("#edit")!, cssVar: "--right-w", hideClass: "no-right", min: 280, max: 640, def: 360, axis: "x", invert: true },
  "edit-bottom": { host: () => document.querySelector("#edit .center")!, cssVar: "--bottom-h", hideClass: "no-bottom", min: 120, max: 520, def: 230, axis: "y", invert: true },
  "subs-left": { host: () => document.querySelector("#subs")!, cssVar: "--left-w", hideClass: "no-left", min: 220, max: 560, def: 320, axis: "x", invert: false },
  "subs-right": { host: () => document.querySelector("#subs")!, cssVar: "--right-w", hideClass: "no-right", min: 260, max: 560, def: 320, axis: "x", invert: true },
};

const listeners: (() => void)[] = [];

/** onLayoutChange runs fn after a panel is shown, hidden or resized. */
export function onLayoutChange(fn: () => void) {
  listeners.push(fn);
}

export function initLayout() {
  for (const [name, s] of Object.entries(spec) as [PanelName, (typeof spec)[PanelName]][]) {
    s.host().style.setProperty(s.cssVar, `${store.get(`size.${name}`, s.def)}px`);
    s.host().classList.toggle(s.hideClass, !store.get(`show.${name}`, true));
  }
  for (const splitter of document.querySelectorAll<HTMLElement>(".splitter[data-resize]")) {
    const name = splitter.dataset.resize as PanelName;
    const s = spec[name];
    splitter.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      splitter.setPointerCapture(e.pointerId);
      splitter.classList.add("dragging");
      const startPos = s.axis === "x" ? e.clientX : e.clientY;
      const startSize = parseFloat(getComputedStyle(s.host()).getPropertyValue(s.cssVar)) || s.def;
      const move = (ev: PointerEvent) => {
        const delta = (s.axis === "x" ? ev.clientX : ev.clientY) - startPos;
        const size = Math.round(Math.min(s.max, Math.max(s.min, startSize + (s.invert ? -delta : delta))));
        s.host().style.setProperty(s.cssVar, `${size}px`);
      };
      const up = () => {
        splitter.classList.remove("dragging");
        splitter.removeEventListener("pointermove", move);
        splitter.removeEventListener("pointerup", up);
        store.set(`size.${name}`, parseFloat(s.host().style.getPropertyValue(s.cssVar)));
        listeners.forEach((fn) => fn());
      };
      splitter.addEventListener("pointermove", move);
      splitter.addEventListener("pointerup", up);
    });
    splitter.addEventListener("dblclick", () => {
      s.host().style.setProperty(s.cssVar, `${s.def}px`);
      store.set(`size.${name}`, s.def);
    });
  }
}

export function isShown(name: PanelName) {
  return !spec[name].host().classList.contains(spec[name].hideClass);
}

export function togglePanel(name: PanelName): boolean {
  const s = spec[name];
  const show = !isShown(name);
  s.host().classList.toggle(s.hideClass, !show);
  store.set(`show.${name}`, show);
  listeners.forEach((fn) => fn());
  return show;
}

export { store };
export type { PanelName };
