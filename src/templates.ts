import type { Channel } from "mygo-runtime";
import { Studio, type Event, type PresetQuestion, type Project, type Template, type View } from "./mygo";
import { $, attempt, el, message, toast } from "./ui";

type Hooks = {
  project(): Project | null;
  setView(v: View | null | undefined): void;
  run<T>(kind: "analyze", fn: (ch: Channel<Event>) => Promise<T>): Promise<T | undefined>;
};

const USES: Record<string, string> = {
  keep: "保留",
  drop: "去掉",
  boost: "加分",
  marker: "只做节点",
  opening: "开局",
  ending: "结尾",
};
const USE_HELP =
  "用途：保留＝命中就保留（有保留题时只留命中的）；去掉＝命中就剪掉，优先于保留；加分＝限时长时优先保留；只做节点＝只给动效做标记；开局 / 结尾＝保留最前 / 最后一段连续命中。";
const TYPES: Record<string, string> = { noul: "是非题", choice: "选择题", score: "打分题" };
const MARKERS: Record<string, string> = { "": "无", opening: "开场", transition: "转场 / 节点", highlight: "高能", ending: "结尾" };

let list: Template[] = [];
let draft: Template | null = null;
let hooks: Hooks;

export async function initTemplates(h: Hooks) {
  hooks = h;
  await refresh();
  $("#tpl-manage").onclick = () => openManager();
  $("#tpl-apply").onclick = apply;
  $("#tpl-save").onclick = saveFromProject;
  $("#tpl-new").onclick = () => edit(blank());
  $("#tpl-import").onclick = async () => {
    const t = await attempt(() => Studio.importTemplate());
    if (t) {
      await refresh();
      edit(t);
      toast(`已导入「${t.name}」`);
    }
  };
  $("#tpl-export").onclick = async () => {
    if (!draft?.id) return toast("先保存模板再导出", true);
    const path = await attempt(() => Studio.exportTemplate(draft!.id));
    if (path) toast(`已导出到 ${path}`);
  };
  $("#tpl-duplicate").onclick = () => {
    if (!draft) return;
    edit({ ...structuredClone(collect()), id: "", builtin: false, name: draft.name + " 副本" });
  };
  $("#tpl-delete").onclick = async () => {
    if (!draft?.id || draft.builtin) return;
    if (!confirm(`删除模板「${draft.name}」？`)) return;
    if ((await attempt(() => Studio.deleteTemplate(draft!.id))) === undefined) return;
    await refresh();
    edit(list[0]!);
  };
  $("#tpl-close").onclick = () => $<HTMLDialogElement>("#tpl-dialog").close();
  $("#tpl-save-btn").onclick = save;
}

export async function refresh() {
  list = ((await attempt(() => Studio.templates())) ?? []).filter((t): t is Template => !!t);
  renderPicker();
  renderList();
  const sel = $<HTMLSelectElement>("#s-template");
  const current = sel.value;
  sel.replaceChildren(el("option", { value: "", textContent: "不套用" }), ...list.map((t) => el("option", { value: t.id, textContent: t.name })));
  sel.value = current;
}

export function renderPicker() {
  const p = hooks?.project();
  const sel = $<HTMLSelectElement>("#tpl-select");
  const keep = sel.value || p?.templateId || "";
  const groups = new Map<string, Template[]>();
  for (const t of list) groups.set(t.category || "其他", [...(groups.get(t.category || "其他") ?? []), t]);
  sel.replaceChildren(
    el("option", { value: "", textContent: "选择模板…" }),
    ...[...groups].map(([cat, ts]) => {
      const g = el("optgroup", { label: cat });
      g.append(...ts.map((t) => el("option", { value: t.id, textContent: t.name })));
      return g;
    }),
  );
  sel.value = list.some((t) => t.id === keep) ? keep : "";
  $("#tpl-current").textContent = p?.templateName ? `当前模板：${p.templateName}` : "";
}

/** Examples to suggest in the chat for the project's template. */
export function examplesFor(p: Project | null): string[] {
  return list.find((t) => t.id === p?.templateId)?.examples ?? [];
}

async function apply() {
  const p = hooks.project();
  const id = $<HTMLSelectElement>("#tpl-select").value;
  if (!p) return;
  if (!id) return toast("先选择一个模板", true);
  const v = await hooks.run("analyze", (ch) => Studio.applyTemplate(p.id, id, ch));
  hooks.setView(v ?? (await attempt(() => Studio.open(p.id))));
  if (v) toast(`已套用：成片 ${Math.round(v.summary.keptSeconds)} 秒，${v.summary.clips} 个片段`);
}

async function saveFromProject() {
  const p = hooks.project();
  if (!p) return;
  const name = prompt("模板名称", p.templateName ? `${p.templateName}（改）` : "我的模板");
  if (!name) return;
  const t = await attempt(() => Studio.saveProjectAsTemplate(p.id, name));
  if (!t) return;
  await refresh();
  toast(`已存为模板「${t.name}」`);
  openManager(t.id);
}

// ---- manager ----

function openManager(id?: string) {
  const d = $<HTMLDialogElement>("#tpl-dialog");
  const pick = list.find((t) => t.id === (id ?? $<HTMLSelectElement>("#tpl-select").value)) ?? list[0];
  if (pick) edit(pick);
  d.showModal();
}

function renderList() {
  const box = $("#tpl-items");
  box.replaceChildren(
    ...list.map((t) => {
      const item = el(
        "div",
        { class: "tpl-item" + (draft?.id === t.id ? " active" : "") },
        el("span", { textContent: t.name }),
        el("small", { textContent: `${t.builtin ? "内置" : "我的"} · ${t.questions.length} 个问题` }),
      );
      item.onclick = () => edit(t);
      return item;
    }),
  );
}

function blank(): Template {
  return {
    version: 1, id: "", name: "新模板", description: "", category: "我的模板", builtin: false,
    questions: [newQuestion(1)], segmentSeconds: 15, targetSeconds: 0, padSeconds: 2, fillGaps: true,
    interestThreshold: 2.5, examples: [], updatedAt: "",
  };
}

function newQuestion(n: number): PresetQuestion {
  return { key: `q${n}`, label: "新问题", type: "noul", instructions: "", use: "keep", threshold: 0.5 };
}

function edit(t: Template) {
  draft = structuredClone(t);
  $("#tpl-error").textContent = "";
  renderForm();
  renderList();
  $<HTMLButtonElement>("#tpl-delete").disabled = !t.id || t.builtin;
  $<HTMLButtonElement>("#tpl-export").disabled = !t.id;
  $<HTMLButtonElement>("#tpl-save-btn").textContent = t.builtin ? "另存为我的模板" : "保存";
}

const input = (name: string, value: string | number, attrs: Partial<HTMLInputElement> = {}) =>
  el("input", { name, value: String(value), ...attrs });

function renderForm() {
  if (!draft) return;
  const t = draft;
  const form = $("#tpl-form");
  const head = el(
    "div",
    { class: "form-grid" },
    el("label", {}, "名称", input("name", t.name)),
    el("label", {}, "分类", input("category", t.category)),
    el("label", {}, "分段长度（秒）", input("segmentSeconds", t.segmentSeconds, { type: "number", min: "5", max: "60" })),
    el("label", {}, "时长上限（秒，0 不限）", input("targetSeconds", t.targetSeconds, { type: "number", min: "0", step: "10" })),
    el("label", {}, "片段前后多留（秒）", input("padSeconds", t.padSeconds, { type: "number", min: "0", max: "10", step: "0.5" })),
    el("label", {}, "无保留题时的精彩门槛（0–4）", input("interestThreshold", t.interestThreshold, { type: "number", min: "0", max: "4", step: "0.1" })),
    el("label", { class: "check" }, el("input", { name: "fillGaps", type: "checkbox", checked: t.fillGaps }), "补齐片段间的小空隙"),
  );
  const desc = el("label", { class: "field" }, "说明", el("textarea", { name: "description", rows: 2, value: t.description }));
  const examples = el(
    "label",
    { class: "field" },
    "聊天快捷语（每行一句）",
    el("textarea", { name: "examples", rows: 2, value: (t.examples ?? []).join("\n") }),
  );
  const qs = t.questions.map((q, i) => questionCard(q, i));
  const add = el("button", { type: "button", textContent: "＋ 添加问题" });
  add.onclick = () => {
    collect();
    draft!.questions.push(newQuestion(draft!.questions.length + 1));
    renderForm();
  };
  const note = el("p", {
    class: "hint muted",
    textContent: "给模型的问题建议用英文写，描述画面或声音里能看到 / 听到的东西；名称会显示在剪辑要求里。改了问题内容会重新分析这一题。" + USE_HELP,
  });
  form.replaceChildren(head, desc, examples, el("h3", { textContent: `问题（${t.questions.length}）` }), note, ...qs, add);
  if (t.builtin) form.prepend(el("p", { class: "hint", textContent: "内置模板不能直接修改，保存时会另存为你的模板。" }));
}

function questionCard(q: PresetQuestion, i: number) {
  const sel = (name: string, options: Record<string, string>, value: string) => {
    const s = el("select", { name });
    s.append(...Object.entries(options).map(([v, label]) => el("option", { value: v, textContent: label })));
    s.value = value;
    return s;
  };
  const type = sel("type", TYPES, q.type);
  type.onchange = () => {
    collect();
    const cur = draft!.questions[i]!;
    if (cur.type === "choice" && !cur.options) cur.options = { yes: "", no: "" };
    if (cur.type === "score" && !cur.levels) cur.levels = ["低", "中", "高"];
    renderForm();
  };
  const remove = el("button", { type: "button", class: "small danger", textContent: "删除" });
  remove.onclick = () => {
    collect();
    draft!.questions.splice(i, 1);
    renderForm();
  };
  const up = el("button", { type: "button", class: "small", textContent: "↑", disabled: i === 0 });
  up.onclick = () => {
    collect();
    const qs = draft!.questions;
    [qs[i - 1], qs[i]] = [qs[i]!, qs[i - 1]!];
    renderForm();
  };
  const card = el(
    "div",
    { class: "q-card" },
    el("div", { class: "q-head" }, el("span", { class: "badge", textContent: `#${i + 1}` }), input("label", q.label, { placeholder: "名称，如：击杀" }), up, remove),
    el(
      "div",
      { class: "form-grid" },
      el("label", {}, "标识", input("key", q.key)),
      el("label", {}, "题型", type),
      el("label", {}, "用途", sel("use", USES, q.use)),
      el(
        "label",
        {},
        q.type === "score" ? "门槛（等级）" : "门槛（概率 0–1）",
        input("threshold", q.threshold, { type: "number", step: q.type === "score" ? "0.5" : "0.05", min: "0" }),
      ),
      el("label", {}, "动效节点", sel("marker", MARKERS, q.marker ?? "")),
    ),
    el("label", { class: "field" }, "给模型的问题", el("textarea", { name: "instructions", rows: 2, value: q.instructions })),
  );
  if (q.type === "noul") {
    card.append(
      el(
        "div",
        { class: "form-grid" },
        el("label", {}, "“是”的含义（可选）", input("yes", q.yes ?? "")),
        el("label", {}, "“否”的含义（可选）", input("no", q.no ?? "")),
      ),
    );
  }
  if (q.type === "choice") {
    const opts = Object.entries(q.options ?? {}).map(([k, v]) => `${k}: ${v}`).join("\n");
    const ta = el("textarea", { name: "options", rows: 4, value: opts });
    const matches = el("div", { class: "q-matches" });
    const drawMatches = () => {
      const keys = parseOptions(ta.value);
      const chosen = new Set(q.match ?? []);
      matches.replaceChildren(
        el("span", { class: "muted", textContent: "算命中的选项：" }),
        ...Object.keys(keys).map((k) =>
          el("label", { class: "check" }, el("input", { type: "checkbox", name: "match", value: k, checked: chosen.has(k) }), k),
        ),
      );
    };
    ta.oninput = () => {
      q.match = [...matches.querySelectorAll<HTMLInputElement>("input:checked")].map((c) => c.value);
      drawMatches();
    };
    drawMatches();
    card.append(el("label", { class: "field" }, "选项（每行一个，“标识: 描述”）", ta), matches);
  }
  if (q.type === "score") {
    card.append(el("label", { class: "field" }, "等级（每行一个，从低到高）", el("textarea", { name: "levels", rows: 4, value: (q.levels ?? []).join("\n") })));
  }
  return card;
}

function parseOptions(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const m = line.match(/^\s*([^:：]+?)\s*[:：]\s*(.*)$/) ?? line.match(/^\s*(\S+)\s*$/);
    if (m) out[m[1]!] = (m[2] ?? "").trim();
  }
  return out;
}

/** collect reads the form back into the draft. */
function collect(): Template {
  const t = draft!;
  const form = $("#tpl-form");
  const val = (root: ParentNode, name: string) => root.querySelector<HTMLInputElement>(`[name="${name}"]`)?.value ?? "";
  const num = (root: ParentNode, name: string) => Number(val(root, name)) || 0;
  t.name = val(form, "name").trim();
  t.category = val(form, "category").trim() || "我的模板";
  t.description = val(form, "description");
  t.segmentSeconds = num(form, "segmentSeconds");
  t.targetSeconds = num(form, "targetSeconds");
  t.padSeconds = num(form, "padSeconds");
  t.interestThreshold = num(form, "interestThreshold");
  t.fillGaps = form.querySelector<HTMLInputElement>('[name="fillGaps"]')!.checked;
  t.examples = val(form, "examples").split("\n").map((s) => s.trim()).filter(Boolean);
  const cards = [...form.querySelectorAll(".q-card")];
  t.questions = cards.map((c) => {
    const q: PresetQuestion = {
      key: val(c, "key").trim(),
      label: val(c, "label").trim(),
      type: val(c, "type"),
      use: val(c, "use"),
      threshold: num(c, "threshold"),
      instructions: val(c, "instructions").trim(),
    };
    const marker = val(c, "marker");
    if (marker) q.marker = marker;
    if (q.type === "noul") {
      if (val(c, "yes")) q.yes = val(c, "yes");
      if (val(c, "no")) q.no = val(c, "no");
    }
    if (q.type === "choice") {
      q.options = parseOptions(val(c, "options"));
      q.match = [...c.querySelectorAll<HTMLInputElement>('[name="match"]:checked')].map((m) => m.value);
    }
    if (q.type === "score") q.levels = val(c, "levels").split("\n").map((s) => s.trim()).filter(Boolean);
    return q;
  });
  return t;
}

async function save() {
  if (!draft) return;
  const t = collect();
  try {
    const saved = await Studio.saveTemplate(t);
    if (!saved) return;
    await refresh();
    edit(saved);
    $<HTMLSelectElement>("#tpl-select").value = saved.id;
    toast(`已保存「${saved.name}」`);
  } catch (err) {
    $("#tpl-error").textContent = message(err);
  }
}
