import { Studio, type ModelRef, type ModelSettings, type ProviderInput, type ProviderView, type Quality, type TestResult } from "./mygo";
import { $, attempt, el, message, toast } from "./ui";

const PRESETS: { name: string; protocol: string; baseUrl: string }[] = [
  { name: "OpenAI", protocol: "openai", baseUrl: "https://api.openai.com/v1" },
  { name: "阿里云百炼（Qwen）", protocol: "openai", baseUrl: "https://dashscope.aliyuncs.com/compatible-mode/v1" },
  { name: "DeepSeek", protocol: "openai", baseUrl: "https://api.deepseek.com/v1" },
  { name: "硅基流动", protocol: "openai", baseUrl: "https://api.siliconflow.cn/v1" },
  { name: "OpenRouter", protocol: "openai", baseUrl: "https://openrouter.ai/api/v1" },
  { name: "Ollama（本机）", protocol: "openai", baseUrl: "http://localhost:11434/v1" },
  { name: "Anthropic", protocol: "anthropic", baseUrl: "https://api.anthropic.com/v1" },
];
const PROTOCOLS: Record<string, string> = { openai: "OpenAI 兼容", anthropic: "Anthropic", "workers-ai": "Cloudflare Workers AI" };
const ROLES: { key: "chat" | "fixer" | "vision"; label: string; hint: string }[] = [
  { key: "chat", label: "聊天模型", hint: "在聊天里和你对话，并调用剪辑操作。需要支持工具调用（function calling）。" },
  { key: "fixer", label: "字幕纠错模型", hint: "发现字幕错误后给出修改，适合小而快的模型，例如 Qwen Flash。" },
  { key: "vision", label: "读画面字幕的模型", hint: "画面里有烧录字幕时读出上面的字，需要支持图片输入。" },
];

let state: ModelSettings | null = null;
let editing: (ProviderInput & { fetched?: string[] }) | null = null;
let onChange: () => void = () => {};

export function initSettings(changed: () => void) {
  onChange = changed;
  for (const b of document.querySelectorAll<HTMLButtonElement>(".settings-nav button")) {
    b.onclick = () => showTab(b.dataset.tab!);
  }
  $("#settings-close").onclick = () => $<HTMLDialogElement>("#settings").close();
  $("#s-save").onclick = saveGeneral;
}

function showTab(tab: string) {
  for (const b of document.querySelectorAll<HTMLButtonElement>(".settings-nav button")) b.classList.toggle("active", b.dataset.tab === tab);
  for (const p of document.querySelectorAll<HTMLElement>("[data-pane]")) p.hidden = p.dataset.pane !== tab;
}

export async function openSettings(tab = "models") {
  showTab(tab);
  $<HTMLDialogElement>("#settings").showModal();
  $("#models-pane").replaceChildren(el("p", { class: "muted", textContent: "正在检测…" }));
  const [ms, gs, tpls] = await Promise.all([attempt(() => Studio.getModelSettings()), attempt(() => Studio.getSettings()), attempt(() => Studio.templates())]);
  if (gs) {
    $<HTMLSelectElement>("#s-concurrency").value = String(gs.concurrency);
    $<HTMLSelectElement>("#s-quality").value = gs.quality;
    const sel = $<HTMLSelectElement>("#s-template");
    sel.replaceChildren(el("option", { value: "", textContent: "不套用" }), ...(tpls ?? []).filter(Boolean).map((t) => el("option", { value: t!.id, textContent: t!.name })));
    sel.value = gs.defaultTemplate;
    const ff = $("#ffmpeg-status");
    ff.textContent = gs.ffmpeg || "ffmpeg 已就绪";
    ff.className = "hint " + (gs.ffmpeg ? "bad" : "good");
  }
  if (ms) {
    state = ms;
    editing = null;
    render();
  }
}

async function saveGeneral() {
  const s = await attempt(() =>
    Studio.saveSettings(Number($<HTMLSelectElement>("#s-concurrency").value), $<HTMLSelectElement>("#s-quality").value as Quality, $<HTMLSelectElement>("#s-template").value),
  );
  if (s) toast("已保存");
}

// ---- models pane ----

function render() {
  if (!state) return;
  const pane = $("#models-pane");
  pane.replaceChildren(
    el("h2", { textContent: "模型" }),
    el("p", { class: "hint muted", textContent: "视频理解固定用 Cloudflare Clef；聊天和字幕纠错可以接你自己的模型服务（OpenAI 兼容 / Anthropic 协议），例如 API 中转站、阿里云百炼、DeepSeek、本机 Ollama。密钥只保存在这台电脑上。" }),
    cloudflareSection(),
    rolesSection(),
    providersSection(),
  );
  if (editing) pane.append(providerEditor());
}

function testBox() {
  return el("div", { class: "test-out", hidden: true });
}

function showTest(box: HTMLElement, r: TestResult | undefined) {
  box.hidden = false;
  if (!r) {
    box.className = "test-out bad";
    box.textContent = "测试失败";
    return;
  }
  box.className = "test-out " + (r.ok ? "good" : "bad");
  box.textContent = r.ok ? `✓ ${r.model} · ${r.latencyMs} ms\n${r.reply}` : `✗ ${r.error}`;
}

function cloudflareSection() {
  const cf = state!.cloudflare;
  const badge = el("span", { class: "badge " + (cf.connected ? "good" : "bad"), textContent: cf.connected ? `已连接 · ${cf.using}` : "未连接" });
  const cli = cf.cli.found
    ? cf.cli.signedIn
      ? `检测到本机 cf 命令行，已登录（${cf.cli.account || cf.cli.accountId}）`
      : "检测到本机 cf 命令行，但还没有登录：在终端运行 cf auth login"
    : "没有检测到 cf 命令行，可以填写 API Token";
  const radio = (value: string, label: string) =>
    el("label", { class: "check" }, el("input", { type: "radio", name: "cf-mode", value, checked: cf.mode === value }), label);
  const account = el("input", { value: cf.accountId, placeholder: cf.cli.accountId || "32 位 Account ID", spellcheck: false });
  const token = el("input", { type: "password", placeholder: cf.hasToken ? `已保存 ${cf.keyHint}（留空则不修改）` : "需要 Workers AI 权限", spellcheck: false });
  const out = testBox();
  const save = el("button", { textContent: "保存" });
  const test = el("button", { textContent: "测试连接" });
  const mode = () => document.querySelector<HTMLInputElement>('input[name="cf-mode"]:checked')?.value ?? "auto";
  save.onclick = async () => {
    const ms = await attempt(() => Studio.saveCloudflare(mode(), account.value, token.value, false));
    if (ms) {
      state = ms;
      render();
      onChange();
      toast("已保存 Cloudflare 连接");
    }
  };
  test.onclick = async () => {
    await attempt(() => Studio.saveCloudflare(mode(), account.value, token.value, false));
    out.hidden = false;
    out.className = "test-out";
    out.textContent = "测试中…";
    showTest(out, await attempt(() => Studio.testCloudflare()));
    const ms = await attempt(() => Studio.getModelSettings());
    if (ms) {
      state = ms;
      onChange();
    }
  };
  return el(
    "div",
    { class: "section" },
    el("div", { class: "section-head" }, el("h3", { textContent: "Cloudflare（Clef 视频理解、Workers AI）" }), badge),
    el("p", { class: "hint muted", textContent: cli }),
    el("div", { class: "radio-row" }, radio("auto", "自动（有 cf 用 cf，否则用 Token）"), radio("cli", "使用本机 cf 命令行"), radio("token", "使用 API Token")),
    el("div", { class: "grid2" }, el("label", {}, "Account ID", account), el("label", {}, "API Token", token)),
    el("div", { class: "actions" }, save, test),
    out,
  );
}

function modelOptions(select: HTMLSelectElement, current: ModelRef) {
  select.replaceChildren();
  for (const p of state!.providers) {
    const g = el("optgroup", { label: p.name });
    for (const m of p.models ?? []) g.append(el("option", { value: `${p.id}\u0000${m}`, textContent: m }));
    if ((p.models ?? []).length) select.append(g);
  }
  const value = `${current.provider}\u0000${current.model}`;
  if (![...select.options].some((o) => o.value === value) && current.model) {
    select.append(el("option", { value, textContent: `${current.model}（${current.provider}）` }));
  }
  select.value = value;
}

const refOf = (v: string): ModelRef => {
  const [provider, model] = v.split("\u0000");
  return { provider: provider ?? "", model: model ?? "" };
};

function rolesSection() {
  const box = el("div", { class: "section" }, el("div", { class: "section-head" }, el("h3", { textContent: "模型用途" })));
  const selects: Record<string, HTMLSelectElement> = {};
  for (const role of ROLES) {
    const sel = el("select");
    modelOptions(sel, state![role.key]);
    selects[role.key] = sel;
    const out = testBox();
    const test = el("button", { textContent: "测试" });
    test.onclick = async () => {
      const ref = refOf(sel.value);
      const p = state!.providers.find((x) => x.id === ref.provider);
      if (!p) return;
      out.hidden = false;
      out.className = "test-out";
      out.textContent = "测试中…";
      showTest(out, await attempt(() => Studio.testModel(toInput(p), ref.model)));
    };
    sel.onchange = async () => {
      const ms = await attempt(() => Studio.setRoles(refOf(selects.chat!.value), refOf(selects.fixer!.value), refOf(selects.vision!.value)));
      if (ms) {
        state = ms;
        onChange();
        toast(`${role.label}已切换为 ${refOf(sel.value).model}`);
      }
    };
    box.append(el("label", {}, role.label, el("div", { class: "model-pick" }, sel, test), el("span", { class: "hint muted", textContent: role.hint })), out);
  }
  return box;
}

function toInput(p: ProviderView): ProviderInput {
  return { id: p.id, name: p.name, protocol: p.protocol, baseUrl: p.baseUrl, apiKey: "", models: p.models ?? [] };
}

function providersSection() {
  const add = el("button", { class: "small", textContent: "＋ 添加服务" });
  add.onclick = () => {
    editing = { id: "", name: "", protocol: "openai", baseUrl: "", apiKey: "", models: [] };
    render();
    $("#models-pane").lastElementChild?.scrollIntoView({ behavior: "smooth" });
  };
  const list = el("div", { class: "provider-list" });
  for (const p of state!.providers) {
    const edit = el("button", { class: "small", textContent: p.builtin ? "查看" : "编辑" });
    edit.onclick = () => {
      editing = toInput(p);
      render();
      $("#models-pane").lastElementChild?.scrollIntoView({ behavior: "smooth" });
    };
    const row = el(
      "div",
      { class: "provider-item" },
      el(
        "div",
        {},
        el("div", { class: "name" }, p.name, " ", el("span", { class: "badge", textContent: PROTOCOLS[p.protocol] ?? p.protocol })),
        el("div", { class: "meta", textContent: p.builtin ? "使用上面的 Cloudflare 连接" : `${p.baseUrl} · ${p.hasKey ? "密钥 " + p.keyHint : "无密钥"} · ${(p.models ?? []).length} 个模型` }),
      ),
      edit,
    );
    if (!p.builtin) {
      const del = el("button", { class: "small danger", textContent: "删除" });
      del.onclick = async () => {
        if (!confirm(`删除模型服务「${p.name}」？`)) return;
        const ms = await attempt(() => Studio.deleteProvider(p.id));
        if (ms) {
          state = ms;
          render();
          onChange();
        }
      };
      row.append(del);
    }
    list.append(row);
  }
  return el("div", { class: "section" }, el("div", { class: "section-head" }, el("h3", { textContent: "模型服务" }), add), list);
}

function providerEditor() {
  const e = editing!;
  const builtin = e.id === "cloudflare";
  const preset = el("select");
  preset.append(el("option", { value: "", textContent: "从常用服务填入…" }), ...PRESETS.map((p, i) => el("option", { value: String(i), textContent: p.name })));
  const name = el("input", { value: e.name, placeholder: "例如：我的中转站", disabled: builtin });
  const protocol = el("select", { disabled: builtin });
  protocol.append(...Object.entries(PROTOCOLS).filter(([k]) => builtin || k !== "workers-ai").map(([k, v]) => el("option", { value: k, textContent: v })));
  protocol.value = e.protocol;
  const base = el("input", { type: "url", value: e.baseUrl, placeholder: "https://api.example.com/v1", spellcheck: false, disabled: builtin });
  const existing = state!.providers.find((p) => p.id === e.id);
  const key = el("input", { type: "password", placeholder: existing?.hasKey ? `已保存 ${existing.keyHint}（留空则不修改）` : "sk-…", spellcheck: false, disabled: builtin });
  preset.onchange = () => {
    const p = PRESETS[Number(preset.value)];
    if (!p) return;
    name.value ||= p.name;
    protocol.value = p.protocol;
    base.value = p.baseUrl;
  };
  const models = new Set(e.models ?? []);
  const modelBox = el("div", { class: "q-matches" });
  const drawModels = (all: string[]) => {
    const names = [...new Set([...all, ...models])].sort();
    modelBox.replaceChildren(
      ...(names.length
        ? names.map((m) => {
            const cb = el("input", { type: "checkbox", checked: models.has(m), disabled: builtin });
            cb.onchange = () => (cb.checked ? models.add(m) : models.delete(m));
            return el("label", { class: "check" }, cb, m);
          })
        : [el("span", { class: "muted", textContent: "点“获取模型列表”，或在右边手动添加。" })]),
    );
  };
  drawModels(e.fetched ?? []);
  const input = (): ProviderInput => ({ id: e.id, name: name.value, protocol: protocol.value, baseUrl: base.value, apiKey: key.value, models: [...models] });
  const fetchBtn = el("button", { class: "small", textContent: "获取模型列表", disabled: builtin });
  fetchBtn.onclick = async () => {
    fetchBtn.textContent = "获取中…";
    try {
      const list = await Studio.fetchModels(input());
      e.fetched = list.filter((x): x is string => !!x);
      drawModels(e.fetched);
      toast(`找到 ${e.fetched.length} 个模型，勾选要用的`);
    } catch (err) {
      toast(message(err), true);
    } finally {
      fetchBtn.textContent = "获取模型列表";
    }
  };
  const manual = el("input", { placeholder: "手动添加模型名，回车", disabled: builtin });
  manual.onkeydown = (ev) => {
    if (ev.key === "Enter" && manual.value.trim()) {
      ev.preventDefault();
      models.add(manual.value.trim());
      manual.value = "";
      drawModels(e.fetched ?? []);
    }
  };
  const testModel = el("select");
  const refreshTestModels = () => {
    testModel.replaceChildren(...[...models].map((m) => el("option", { value: m, textContent: m })));
  };
  refreshTestModels();
  modelBox.addEventListener("change", refreshTestModels);
  manual.addEventListener("keydown", () => setTimeout(refreshTestModels));
  const out = testBox();
  const test = el("button", { textContent: "测试" });
  test.onclick = async () => {
    if (!testModel.value) return toast("先勾选或添加一个模型", true);
    out.hidden = false;
    out.className = "test-out";
    out.textContent = "测试中…";
    showTest(out, await attempt(() => Studio.testModel(input(), testModel.value)));
  };
  const save = el("button", { class: "primary", textContent: "保存服务", disabled: builtin });
  save.onclick = async () => {
    const ms = await attempt(() => Studio.saveProvider(input()));
    if (ms) {
      state = ms;
      editing = null;
      render();
      onChange();
      toast("已保存模型服务");
    }
  };
  const cancel = el("button", { textContent: "取消" });
  cancel.onclick = () => {
    editing = null;
    render();
  };
  return el(
    "div",
    { class: "section" },
    el("div", { class: "section-head" }, el("h3", { textContent: builtin ? "Cloudflare Workers AI" : e.id ? `编辑：${e.name}` : "添加模型服务" }), builtin ? "" : preset),
    builtin ? el("p", { class: "hint muted", textContent: "内置服务使用上面的 Cloudflare 连接，不需要单独的密钥。" }) : "",
    el("div", { class: "grid2" }, el("label", {}, "名称", name), el("label", {}, "协议", protocol)),
    el("label", {}, "Base URL", base),
    el("label", {}, "API Key", key),
    el("div", { class: "section-head" }, el("span", { class: "muted", textContent: "模型" }), el("div", { class: "actions" }, manual, fetchBtn)),
    modelBox,
    el("label", {}, "测试", el("div", { class: "model-pick" }, testModel, test)),
    out,
    el("div", { class: "actions end" }, cancel, save),
  );
}
