import { api } from "../api.js";
import { formatAge, knownBuild } from "../format.js";
import { html } from "../html.js";
import { isChinese, t, tOr } from "../i18n.js";
import { icon } from "../icons.js";
import sandboxLabels from "../sandbox-labels.js";
import { describeOptions, encodeSandboxCode, parseSandboxCode, setOption } from "../sandbox-code.js";
import { state } from "../store.js";
import { callout, copyText, pageHeader, toast, withBusy } from "../ui.js";

// Builds already auto-refreshed in this page session; avoids refresh loops.
const refreshedBuilds = new Set();

const errorText = error => (error?.key ? t(error.key, error.vars) : error?.message || String(error));

function tryParse(code) {
  try {
    return { draft: parseSandboxCode(code), error: "" };
  } catch (error) {
    return { draft: null, error: errorText(error) };
  }
}

function optionCard(option) {
  if (!option.editable) {
    return html`<div class="option-card" data-option-card data-category="${option.category}" data-search="${option.search}">
      <strong>${option.title}</strong><small>${option.meta}</small>
      <div class="field"><select disabled aria-label="${option.title}"><option>${option.activeLabel ?? t("currentValue", { value: option.initial })}</option></select></div>
    </div>`;
  }
  const outOfRange = option.initial >= option.choices.length;
  return html`<label class="option-card" data-option-card data-category="${option.category}" data-search="${option.search}">
    <strong>${option.title}</strong><small>${option.meta}</small>
    <span class="field"><select data-option="${option.id}" data-initial="${option.initial}" aria-label="${option.title}">
      ${option.choices.map((choice, index) => html`<option value="${index}" ${index === option.initial ? html`selected` : ""}>${choice}</option>`)}
      ${outOfRange ? html`<option value="${option.initial}" selected>${t("currentValue", { value: option.initial })}</option>` : ""}
    </select></span>
  </label>`;
}

export default {
  async render(ctx) {
    const data = await api.get("/api/sandbox", { signal: ctx.signal });
    const build = state.status.build;
    const visualAvailable = (data.mode === "live" || data.mode === "cache") && knownBuild(data.build) && data.build === build && Array.isArray(data.payload?.options);

    let refreshNeedsToken = false;
    if (!visualAvailable && data.dashboard?.enabled && state.status.state === "running" && knownBuild(build) && !refreshedBuilds.has(build)) {
      refreshedBuilds.add(build);
      try {
        await api.post("/api/sandbox/refresh", {}, { signal: ctx.signal });
        return ctx.rerender();
      } catch (error) {
        if (error?.name === "AbortError") throw error;
        refreshNeedsToken = error.status === 401;
      }
    }

    const records = data.records || [];
    const parsed = tryParse(data.code);
    const loadError = data.warning ? t("sandboxWarning", { warning: data.warning }) : parsed.error;
    const options = visualAvailable && parsed.draft
      ? describeOptions(data.payload, records, data.optionTexts, {
        chinese: isChinese(),
        labels: sandboxLabels,
        fallbackCategory: "Other",
        untranslated: name => t("untranslated", { name }),
        optionLabel: id => t("option", { id }),
      })
      : [];
    const visual = options.length > 0;
    const categories = [...new Set(options.map(option => option.category))];
    const highest = records.map(row => Number(row.OptionID)).filter(Number.isFinite).reduce((max, id) => Math.max(max, id), -1);
    const modeKey = `mode_${String(data.mode).replace(/-/g, "_")}`;
    const showCredentials = !visual || refreshNeedsToken;

    const statusCallout = visual
      ? callout("success", "checkCircle", t("cacheAvailable", { build: data.build, age: formatAge(data.cachedAt) }))
      : callout("warning", "info", html`<strong>${!knownBuild(build) ? t("unknownBuildRaw") : data.build ? t("cacheUnavailable", { build: data.build, age: formatAge(data.cachedAt) }) : t("buildMismatchRaw")}</strong>
          ${t(data.dashboard?.enabled ? "sandboxSetupEnabled" : "sandboxSetupDisabled", { port: data.dashboard?.port || t("notConfigured") })}`);

    ctx.main.innerHTML = html`<div id="sandbox-page">
      ${pageHeader(t("nav_sandbox"), t("sandboxHelp"))}
      <div class="stack-lg">
        ${statusCallout}
        <section class="card">
          <div class="card-header"><h3>SandboxCode</h3>
            <span class="subtle">${t("codeStats", { code: data.code.length, records: records.length, highest: highest >= 0 ? highest : t("none") })} · ${tOr(modeKey, data.mode)}</span></div>
          <div class="card-body">
            <div class="grid ${visual ? "grid-2" : ""}">
              <label class="field"><span>${t("rawCode")}</span><textarea id="raw" spellcheck="false">${data.code}</textarea></label>
              ${visual ? html`<div class="field"><span>${t("modifiedCode")}</span><textarea id="generated" readonly spellcheck="false">${data.code}</textarea>
                <div><button class="button button-secondary button-sm" id="copy-code">${icon("copy")}${t("copyCode")}</button></div></div>` : ""}
            </div>
            <p id="code-feedback" class="field-feedback" role="status"></p>
          </div>
        </section>
        ${visual ? html`<section class="card">
          <div class="card-header"><h3>${t("visualEditor")}</h3>
            <label class="field search"><span class="visually-hidden">${t("filterOptions")}</span>${icon("search")}<input type="search" id="option-search" placeholder="${t("filterOptions")}" autocomplete="off"></label></div>
          <div class="card-body">
            <div class="chip-row" role="group" aria-label="${t("sandboxCategories")}">
              <button class="chip" data-category-filter="" aria-pressed="true">${t("all")}</button>
              ${categories.map(category => html`<button class="chip" data-category-filter="${category}" aria-pressed="false">${isChinese() ? tOr(`category_${category}`, category) : category}</button>`)}
            </div>
            <div class="option-grid">${options.map(optionCard)}</div>
          </div>
        </section>` : ""}
        <section class="card" id="credentials" ${showCredentials ? "" : html`hidden`}>
          <div class="card-header"><h3>${t("refreshDefinitions")}</h3></div>
          <div class="card-body">
            <div class="grid grid-2">
              <label class="field"><span>${t("tokenName")}</span><input id="token-name" type="password" autocomplete="off"></label>
              <label class="field"><span>${t("tokenSecret")}</span><input id="token-secret" type="password" autocomplete="off"></label>
            </div>
            <div><button class="button button-secondary" id="refresh" ${data.dashboard?.enabled ? "" : html`disabled`} data-requires="running">${icon("refresh")}${t("refreshDefinitions")}</button></div>
          </div>
        </section>
      </div>
      <div class="save-bar" id="save-bar" hidden>
        <p id="dirty-label"></p>
        <button class="button button-ghost" id="discard">${t("discardChanges")}</button>
        <button class="button button-primary" id="save">${icon("check")}${t("save")}</button>
      </div>
    </div>`.value;

    // ---------- Editing ----------
    const page = ctx.main.querySelector("#sandbox-page");
    const raw = page.querySelector("#raw");
    const generated = page.querySelector("#generated");
    const feedback = page.querySelector("#code-feedback");
    const selects = [...page.querySelectorAll("[data-option]")];
    const saveBar = page.querySelector("#save-bar");
    const saveButton = page.querySelector("#save");
    let draft = parsed.draft;
    let source = "visual";   // becomes "raw" once the raw code is typed into
    let error = loadError;

    const edits = () => new Map(selects.filter(select => select.value !== select.dataset.initial).map(select => [Number(select.dataset.option), Number(select.value)]));
    const currentCode = () => (generated ? generated.value : raw.value);
    const dirty = () => raw.value !== data.code || currentCode() !== data.code || edits().size > 0;

    const sync = () => {
      for (const select of selects) select.closest("[data-option-card]").classList.toggle("is-dirty", select.value !== select.dataset.initial);
      feedback.textContent = error;
      feedback.className = `field-feedback${error ? " is-error" : ""}`;
      const isDirty = dirty();
      saveBar.hidden = !isDirty && !error;
      saveButton.disabled = Boolean(error) || !isDirty;
      page.querySelector("#dirty-label").textContent = error || (isDirty ? t("unsavedCount", { count: Math.max(edits().size, 1) }) : "");
    };

    raw.addEventListener("input", () => {
      source = "raw";
      const next = tryParse(raw.value);
      draft = next.draft;
      error = next.error;
      if (draft) {
        if (generated) generated.value = raw.value;
        for (const select of selects) {
          const record = draft.records.find(row => row.OptionID === Number(select.dataset.option));
          if (record && select.querySelector(`option[value="${record.ValueIndex}"]`)) select.value = String(record.ValueIndex);
        }
      }
      sync();
    });
    for (const select of selects) {
      select.addEventListener("change", () => {
        if (!draft) return;
        setOption(draft, Number(select.dataset.option), Number(select.value));
        if (generated) generated.value = encodeSandboxCode(draft);
        sync();
      });
    }
    ctx.setGuard(dirty);

    // ---------- Filtering ----------
    let category = "";
    const filter = () => {
      const query = page.querySelector("#option-search")?.value.trim().toLowerCase() || "";
      for (const card of page.querySelectorAll("[data-option-card]")) {
        card.hidden = (category && card.dataset.category !== category) || (query && !card.dataset.search.includes(query));
      }
    };
    page.querySelector("#option-search")?.addEventListener("input", filter);
    for (const chip of page.querySelectorAll("[data-category-filter]")) {
      chip.addEventListener("click", () => {
        category = chip.dataset.categoryFilter;
        for (const other of page.querySelectorAll("[data-category-filter]")) other.setAttribute("aria-pressed", String(other === chip));
        filter();
      });
    }

    // ---------- Actions ----------
    page.querySelector("#copy-code")?.addEventListener("click", () => copyText(currentCode()));
    page.querySelector("#discard").addEventListener("click", () => ctx.rerender());
    page.querySelector("#refresh").addEventListener("click", event => withBusy(event.currentTarget, t("working"), async () => {
      try {
        await api.post("/api/sandbox/refresh", { tokenName: page.querySelector("#token-name").value, tokenSecret: page.querySelector("#token-secret").value });
      } catch (failure) {
        if (failure.status !== 401) throw failure;
        toast(t("sandboxRefreshNeedsToken"), "error");
        return;
      }
      toast(t("definitionsRefreshed"), "success");
      ctx.rerender();
    }));
    saveButton.addEventListener("click", event => {
      if (!draft || error || !dirty()) return;
      const visualOnly = source === "visual";
      const changes = edits();
      if (visualOnly && !changes.size) {
        toast(t("noChanges"));
        return;
      }
      withBusy(event.currentTarget, t("working"), async () => {
        try {
          if (visualOnly) await api.put("/api/sandbox/options", { hash: data.hash, edits: Object.fromEntries(changes) });
          else await api.put("/api/sandbox/raw", { hash: data.hash, code: currentCode(), force: false });
        } catch (failure) {
          if (failure.status === 409 && failure.data?.code !== "sandbox_warning") {
            toast(t("configConflict"), "error");
            ctx.setGuard(null);
            ctx.rerender();
            return;
          }
          throw failure;
        }
        toast(visualOnly ? t("savedKnownOptions") : t("rawSaved"), "success");
        ctx.setGuard(null);
        ctx.rerender();
      });
    });
    sync();
  },
};
