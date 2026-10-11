// Line icons in the style of SF Symbols, drawn at 16×16 with currentColor.
const svg = (body: string, size = 16) =>
  `<svg width="${size}" height="${size}" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`;

export const icons = {
  open: svg('<path d="M2 4.5V12a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1.5Z"/>'),
  undo: svg('<path d="M5.5 4 2.5 7l3 3"/><path d="M2.5 7h7a4 4 0 0 1 0 8H7"/>'),
  sliders: svg('<path d="M3 4h10M3 8h10M3 12h10"/><circle cx="6" cy="4" r="1.4" fill="var(--panel)"/><circle cx="10" cy="8" r="1.4" fill="var(--panel)"/><circle cx="5" cy="12" r="1.4" fill="var(--panel)"/>'),
  template: svg('<rect x="2.5" y="2.5" width="11" height="11" rx="1.5"/><path d="M2.5 6h11M6 6v7.5"/>'),
  export: svg('<path d="M8 10V2.5M5 5.5l3-3 3 3"/><path d="M3 9.5V13h10V9.5"/>'),
  gear: svg('<path d="M6.9 1.8h2.2l.35 1.7c.4.15.77.36 1.1.62l1.65-.55 1.1 1.9-1.3 1.15c.06.4.06.82 0 1.22l1.3 1.15-1.1 1.9-1.65-.55c-.33.26-.7.47-1.1.62L9.1 14.2H6.9l-.35-1.7c-.4-.15-.77-.36-1.1-.62l-1.65.55-1.1-1.9L4 9.38a3.9 3.9 0 0 1 0-1.22L2.7 7l1.1-1.9 1.65.55c.33-.26.7-.47 1.1-.62Z"/><circle cx="8" cy="8" r="2"/>'),
  sidebarLeft: svg('<rect x="1.8" y="2.5" width="12.4" height="11" rx="1.5"/><path d="M6 2.5v11"/>'),
  sidebarRight: svg('<rect x="1.8" y="2.5" width="12.4" height="11" rx="1.5"/><path d="M10 2.5v11"/>'),
  panelBottom: svg('<rect x="1.8" y="2.5" width="12.4" height="11" rx="1.5"/><path d="M1.8 9.5h12.4"/>'),
  send: svg('<path d="M8 13V3M3.5 7.5 8 3l4.5 4.5"/>'),
  stop: svg('<rect x="4.5" y="4.5" width="7" height="7" rx="1" fill="currentColor"/>'),
  sparkles: svg('<path d="M7 2.5 8.2 6 11.5 7 8.2 8 7 11.5 5.8 8 2.5 7 5.8 6Z"/><path d="M12.5 10.5l.5 1.4 1.4.5-1.4.5-.5 1.4-.5-1.4-1.4-.5 1.4-.5Z"/>'),
  film: svg('<rect x="2" y="2.5" width="12" height="11" rx="1.5"/><path d="M5 2.5v11M11 2.5v11M2 6h3M2 10h3M11 6h3M11 10h3"/>'),
  captions: svg('<rect x="1.8" y="3" width="12.4" height="10" rx="1.5"/><path d="M7 7.2a1.6 1.6 0 1 0 0 1.6M11.6 7.2a1.6 1.6 0 1 0 0 1.6"/>'),
  check: svg('<path d="m3.5 8.5 3 3 6-7"/>'),
  close: svg('<path d="m4 4 8 8M12 4l-8 8"/>'),
  play: svg('<path d="M5 3.5v9l7.5-4.5Z" fill="currentColor"/>'),
  eye: svg('<path d="M1.5 8S4 3.5 8 3.5 14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8Z"/><circle cx="8" cy="8" r="2"/>'),
  plus: svg('<path d="M8 3v10M3 8h10"/>'),
  trash: svg('<path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5"/>'),
  scissors: svg('<circle cx="4.5" cy="4.5" r="2"/><circle cx="4.5" cy="11.5" r="2"/><path d="m6.2 5.6 7.3 6.4M6.2 10.4l7.3-6.4"/>'),
  warning: svg('<path d="M8 2.5 14 13H2Z"/><path d="M8 6.5v3M8 11.3v.1"/>'),
  cpu: svg('<rect x="4" y="4" width="8" height="8" rx="1"/><path d="M6 1.5V4M10 1.5V4M6 12v2.5M10 12v2.5M1.5 6H4M1.5 10H4M12 6h2.5M12 10h2.5"/>'),
};

export type IconName = keyof typeof icons;

/** iconButton builds a toolbar button with a native tooltip. */
export function iconButton(name: IconName, title: string, extra = ""): HTMLButtonElement {
  const b = document.createElement("button");
  b.type = "button";
  b.className = `icon ${extra}`.trim();
  b.title = title;
  b.setAttribute("aria-label", title);
  b.innerHTML = icons[name];
  return b;
}
