import { api } from "../api.js";
import { html } from "../html.js";
import { getLanguage, isChinese, t, tOr } from "../i18n.js";
import { icon } from "../icons.js";
import { callout, emptyState, pageHeader, toast, withBusy } from "../ui.js";

const COMMON = new Set([
  "ServerName", "ServerDescription", "ServerPassword", "ServerPort", "ServerVisibility", "ServerMaxPlayerCount",
  "GameWorld", "WorldGenSeed", "WorldGenSize", "GameName", "GameDifficulty", "Language",
  "TelnetEnabled", "TelnetPort", "TelnetPassword", "WebDashboardEnabled", "WebDashboardPort", "UserDataFolder",
]);
const CHOICES = {
  WorldGenSize: ["6144", "8192", "10240"],
  ServerMaxPlayerCount: ["1", "2", "4", "8", "12", "16", "24", "32"],
  ServerMaxWorldTransferSpeedKiBs: ["128", "256", "512", "768", "1024", "1300"],
  ServerMaxAllowedViewDistance: ["6", "8", "10", "12"],
};
const LANGUAGES = ["English", "Chinese", "Japanese", "Korean", "German", "French", "Spanish", "Russian", "Portuguese", "Italian", "Polish", "Turkish", "Arabic", "Thai", "Vietnamese"];
export const SECRET_MASK = "********";

export function settingText(property) {
  const language = getLanguage();
  const labels = property.text?.labels || {};
  const descriptions = property.text?.descriptions || {};
  const label = labels[language] || labels.en || property.name;
  const description = isChinese()
    ? descriptions[language] || tOr(`hint_${property.name}`, "")
    : descriptions.en || property.englishComment || tOr(`hint_${property.name}`, "");
  return { label, description };
}

function control(property, id) {
  const common = html`id="${id}" data-key="${property.name}"`;
  if (property.secret) return html`<input ${common} type="password" value="${property.value}" autocomplete="new-password" aria-describedby="${id}-hint">`;
  if (/^(true|false)$/i.test(property.value)) {
    const on = /^true$/i.test(property.value);
    return html`<select ${common}><option value="true" ${on ? html`selected` : ""}>${t("yes")}</option><option value="false" ${on ? "" : html`selected`}>${t("no")}</option></select>`;
  }
  if (property.name === "ServerVisibility") {
    return html`<select ${common}>${["0", "1", "2"].map(value => html`<option value="${value}" ${value === property.value ? html`selected` : ""}>${value} · ${t(`visibility_${value}`)}</option>`)}</select>`;
  }
  const choices = CHOICES[property.name];
  if (choices) {
    const values = choices.includes(property.value) ? choices : [property.value, ...choices];
    return html`<select ${common}>${values.map(value => html`<option ${value === property.value ? html`selected` : ""}>${value}</option>`)}</select>`;
  }
  if (property.name === "Language") return html`<input ${common} value="${property.value}" list="language-options" autocomplete="off">`;
  return html`<input ${common} value="${property.value}" autocomplete="off" spellcheck="false">`;
}

function settingRow(property, index) {
  const id = `setting-${index}`;
  const { label, description } = settingText(property);
  const search = `${label} ${property.name} ${description}`.toLowerCase();
  return html`<li class="setting" data-setting data-search="${search}">
    <div class="setting-text">
      <label for="${id}">${label}</label>
      ${description ? html`<p>${description}</p>` : ""}
      ${property.secret ? html`<p id="${id}-hint">${t("secretHint")}</p>` : ""}
      <code class="setting-key">${property.name}</code>
    </div>
    <div class="field">${control(property, id)}</div>
  </li>`;
}

const section = (title, properties, offset) => html`<section class="card" data-section>
  <div class="card-header"><h3>${title}</h3><span class="subtle">${properties.length}</span></div>
  <ul class="setting-list">${properties.map((property, index) => settingRow(property, offset + index))}</ul>
</section>`;

/** Values that differ from what was loaded; unchanged masked secrets are never sent. */
export function collectUpdates(controls, initial) {
  const updates = {};
  for (const input of controls) {
    const key = input.dataset.key;
    if (input.value !== initial.get(key)) updates[key] = input.value;
  }
  return updates;
}

export default {
  async render(ctx) {
    let doc = await api.get("/api/config", { signal: ctx.signal });

    const paint = () => {
      const properties = doc.properties || [];
      const common = properties.filter(property => COMMON.has(property.name));
      const other = properties.filter(property => !COMMON.has(property.name));
      ctx.main.innerHTML = html`<div id="config-page">${pageHeader(t("nav_config"), t("configHelp"))}
        <div data-show-when="running" hidden>${callout("info", "info", t("configRunningNotice"))}</div>
        <div class="config-toolbar">
          <label class="field search"><span class="visually-hidden">${t("searchSettings")}</span>${icon("search")}
            <input type="search" id="config-search" placeholder="${t("searchSettings")}" autocomplete="off"></label>
        </div>
        <div class="stack">
          ${section(t("commonSettings"), common, 0)}
          ${section(t("otherSettings"), other, common.length)}
          <div id="config-empty" hidden>${emptyState("search", t("noMatches"))}</div>
        </div>
        <datalist id="language-options">${LANGUAGES.map(value => html`<option value="${value}">`)}</datalist>
        <div class="save-bar" id="save-bar" hidden>
          <p id="dirty-count" aria-live="polite"></p>
          <button class="button button-ghost" id="discard">${t("discardChanges")}</button>
          <button class="button button-primary" id="save">${icon("check")}${t("saveChanges")}</button>
        </div></div>`.value;
      ctx.gates();
      bind();
    };

    const bind = () => {
      const controls = [...ctx.main.querySelectorAll("[data-key]")];
      const initial = new Map(controls.map(input => [input.dataset.key, input.value]));
      const saveBar = ctx.main.querySelector("#save-bar");
      const dirtyCount = () => Object.keys(collectUpdates(controls, initial)).length;
      const refreshDirty = () => {
        for (const input of controls) input.closest("[data-setting]").classList.toggle("is-dirty", input.value !== initial.get(input.dataset.key));
        const count = dirtyCount();
        saveBar.hidden = count === 0;
        ctx.main.querySelector("#dirty-count").textContent = t("unsavedCount", { count });
      };
      const page = ctx.main.querySelector("#config-page");
      page.addEventListener("input", event => { if (event.target.dataset.key) refreshDirty(); });
      page.addEventListener("change", event => { if (event.target.dataset.key) refreshDirty(); });
      ctx.setGuard(() => dirtyCount() > 0);

      ctx.main.querySelector("#config-search").addEventListener("input", event => {
        const query = event.target.value.trim().toLowerCase();
        let visible = 0;
        for (const row of ctx.main.querySelectorAll("[data-setting]")) {
          row.hidden = Boolean(query) && !row.dataset.search.includes(query);
          if (!row.hidden) visible++;
        }
        for (const card of ctx.main.querySelectorAll("[data-section]")) card.hidden = !card.querySelector("[data-setting]:not([hidden])");
        ctx.main.querySelector("#config-empty").hidden = visible > 0;
      });

      ctx.main.querySelector("#discard").onclick = () => paint();
      ctx.main.querySelector("#save").onclick = event => withBusy(event.currentTarget, t("working"), async () => {
        const updates = collectUpdates(controls, initial);
        try {
          await api.put("/api/config", { hash: doc.hash, updates });
        } catch (error) {
          if (error.status !== 409) throw error;
          toast(t("configConflict"), "error");
          doc = await api.get("/api/config");
          paint();
          return;
        }
        toast(t("saved"), "success");
        doc = await api.get("/api/config");
        paint();
      });
    };

    paint();
  },
};
