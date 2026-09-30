import { api } from "../api.js";
import { html } from "../html.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { callout, emptyState, pageHeader, toast, toastError, withBusy } from "../ui.js";

const filters = { name: "", author: "", status: "all", problem: "all" };

export const modState = mod => (mod.unknown ? "unknown" : mod.enabled ? "enabled" : "disabled");

export function filterMods(mods, { name, author, status, problem }) {
  return mods.filter(mod => {
    const problems = mod.problems || [];
    return (!name || String(mod.name).toLowerCase().includes(name.toLowerCase()))
      && (!author || String(mod.author).toLowerCase().includes(author.toLowerCase()))
      && (status === "all" || modState(mod) === status)
      && (problem === "all"
        || (problem === "dependency" && (mod.dependencies || []).length > 0)
        || (problem === "problem" && problems.length > 0));
  });
}

export function problemText(problem) {
  const key = `problem_${problem.kind}`;
  const vars = { dependency: problem.dependency || "", constraint: problem.constraint || "" };
  const text = t(key, vars);
  return (text === key ? t("problem_unknown") : text).trim();
}

const STATE_BADGE = { enabled: "badge-success", disabled: "", unknown: "badge-warning" };
const STATE_LABEL = { enabled: "modEnabled", disabled: "modDisabled", unknown: "modUnknown" };

function modRow(mod, locked) {
  const status = modState(mod);
  const dependencies = (mod.dependencies || []).map(dependency => `${dependency.id}${dependency.constraint ? ` (${dependency.constraint})` : ""}`);
  return html`<li class="mod-row">
    <div>
      <div class="mod-title"><h4>${mod.name || mod.id || t("unnamedMod")}</h4><span class="badge ${STATE_BADGE[status]}">${t(STATE_LABEL[status])}</span></div>
      <p class="mod-meta">${mod.author || t("unknownAuthor")} · ${mod.version || t("unknownVersion")} · <span class="mono">${mod.key}</span></p>
      <p class="mod-desc">${mod.description || t("noDescription")}</p>
      ${dependencies.length ? html`<p class="mod-meta">${t("dependencies", { list: dependencies.join(", ") })}</p>` : ""}
      ${(mod.problems || []).length ? html`<ul class="mod-problems">${mod.problems.map(problem => html`<li class="badge badge-danger">${icon("alert")}${problemText(problem)}</li>`)}</ul>` : ""}
    </div>
    <div class="mod-side">
      <button class="button ${mod.enabled ? "button-secondary" : "button-primary"} button-sm" data-mod="${mod.key}" data-enable="${!mod.enabled}" ${locked ? html`disabled` : ""}>${mod.enabled ? t("disable") : t("enable")}</button>
    </div>
  </li>`;
}

const select = (id, label, value, options) => html`<label class="field"><span>${label}</span><select id="${id}">
  ${options.map(([optionValue, text]) => html`<option value="${optionValue}" ${optionValue === value ? html`selected` : ""}>${text}</option>`)}</select></label>`;

export default {
  async render(ctx) {
    let catalog = await api.get("/api/mods", { signal: ctx.signal });

    const listHTML = () => {
      const mods = catalog.mods || [];
      const visible = filterMods(mods, filters);
      if (!mods.length) return emptyState("mods", t("noMods"), t("noModsHelp"));
      if (!visible.length) return emptyState("search", t("noModsMatch"));
      return html`<ul class="list">${visible.map(mod => modRow(mod, catalog.locked))}</ul>`;
    };

    const paint = () => {
      const mods = catalog.mods || [];
      const locked = Boolean(catalog.locked);
      ctx.main.innerHTML = html`<div id="mods-page">
        ${pageHeader(t("nav_mods"), t("modsHelp"))}
        <div class="stack-lg">
          ${locked ? callout("warning", "alert", t("modsLocked")) : ""}
          <div class="grid grid-2">
            <section class="card">
              <div class="card-header"><h3>${t("uploadTitle")}</h3></div>
              <div class="card-body">
                <label class="dropzone" id="dropzone" aria-disabled="${locked}">
                  ${icon("upload")}<strong>${t("dropHint")}</strong><span class="subtle">${t("dropHelp")}</span>
                  <input type="file" id="mod-file" accept=".zip" ${locked ? html`disabled` : ""}>
                </label>
              </div>
            </section>
            <section class="card">
              <div class="card-header"><h3>${t("policyTitle")}</h3></div>
              <fieldset class="card-body" ${locked ? html`disabled` : ""}>
                <legend class="visually-hidden">${t("policyTitle")}</legend>
                <div class="segmented">${["warn", "strict", "ignore"].map(policy => html`<label><input type="radio" name="policy" value="${policy}" ${catalog.policy === policy ? html`checked` : ""}>${t(`policy_${policy}`)}</label>`)}</div>
                <p class="subtle" id="policy-help">${t(`policy_${catalog.policy || "warn"}_help`)}</p>
              </fieldset>
            </section>
          </div>
          <section class="card">
            <div class="card-header"><h3>${t("nav_mods")}</h3><span class="subtle">${t("modCount", { count: mods.length, enabled: mods.filter(mod => mod.enabled).length })}</span></div>
            <div class="card-body">
              <div class="filters">
                <label class="field"><span>${t("filterName")}</span><input id="filter-name" value="${filters.name}" autocomplete="off"></label>
                <label class="field"><span>${t("filterAuthor")}</span><input id="filter-author" value="${filters.author}" autocomplete="off"></label>
                ${select("filter-status", t("filterStatus"), filters.status, [["all", t("all")], ["enabled", t("modEnabled")], ["disabled", t("modDisabled")], ["unknown", t("modUnknown")]])}
                ${select("filter-problem", t("filterProblems"), filters.problem, [["all", t("all")], ["dependency", t("hasDependencies")], ["problem", t("hasProblems")]])}
              </div>
            </div>
            <div class="card-body-flush" id="mod-list">${listHTML()}</div>
          </section>
        </div>
      </div>`.value;
      bind();
    };

    const repaintList = () => {
      ctx.main.querySelector("#mod-list").innerHTML = listHTML().value;
      bindToggles();
    };

    const upload = async file => {
      if (!file || !file.name.toLowerCase().endsWith(".zip")) {
        toast(t("chooseZip"), "error");
        return;
      }
      const zone = ctx.main.querySelector("#dropzone");
      zone.setAttribute("aria-busy", "true");
      zone.querySelector("strong").textContent = t("uploading");
      try {
        const form = new FormData();
        form.append("package", file);
        catalog = await api.upload("/api/mods/upload", form);
        toast(t("uploaded", { name: file.name }), "success");
      } catch (error) {
        toastError(error);
      }
      paint();
    };

    const bindToggles = () => {
      for (const button of ctx.main.querySelectorAll("[data-mod]")) {
        button.onclick = () => withBusy(button, t("working"), async () => {
          const enable = button.dataset.enable === "true";
          catalog = await api.put(`/api/mods/${encodeURIComponent(button.dataset.mod)}`, { enabled: enable });
          toast(t(enable ? "modNowEnabled" : "modNowDisabled", { name: button.dataset.mod }), "success");
          repaintList();
        });
      }
    };

    const bind = () => {
      const page = ctx.main.querySelector("#mods-page");
      const zone = page.querySelector("#dropzone");
      page.querySelector("#mod-file").addEventListener("change", event => upload(event.target.files[0]));
      if (!catalog.locked) {
        zone.addEventListener("dragover", event => { event.preventDefault(); zone.classList.add("is-over"); });
        zone.addEventListener("dragleave", () => zone.classList.remove("is-over"));
        zone.addEventListener("drop", event => {
          event.preventDefault();
          zone.classList.remove("is-over");
          upload(event.dataTransfer.files[0]);
        });
      }
      for (const radio of page.querySelectorAll('input[name="policy"]')) {
        radio.addEventListener("change", async () => {
          try {
            catalog = await api.put("/api/mods/policy", { policy: radio.value });
            toast(t("policySaved"), "success");
          } catch (error) {
            toastError(error);
          }
          paint();
        });
      }
      const watch = (id, key, event) => page.querySelector(id).addEventListener(event, target => {
        filters[key] = target.target.value;
        repaintList();
      });
      watch("#filter-name", "name", "input");
      watch("#filter-author", "author", "input");
      watch("#filter-status", "status", "change");
      watch("#filter-problem", "problem", "change");
      bindToggles();
    };

    paint();
    // The lock follows the server state; refetch when it changes.
    let lastState = undefined;
    ctx.on("status", async status => {
      if (lastState !== undefined && status.state !== lastState) {
        catalog = await api.get("/api/mods").catch(() => catalog);
        if (ctx.isCurrent()) paint();
      }
      lastState = status.state;
    });
  },
};
