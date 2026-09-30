import { api } from "../api.js";
import { html } from "../html.js";
import { t, tOr } from "../i18n.js";
import { icon } from "../icons.js";
import { callout, confirmAction, emptyState, pageHeader, toast, withBusy } from "../ui.js";

const PROTOCOLS = ["TCP", "UDP", "Both"];
const PROFILES = ["Any", "Domain", "Private", "Public"];
const label = (kind, value) => tOr(`${kind}_${value}`, String(value || ""));

const options = (kind, values, selected) =>
  values.map(value => html`<option value="${value}" ${value === selected ? html`selected` : ""}>${label(kind, value)}</option>`);

function ruleItem(rule) {
  return html`<li><span><strong>${rule.name || rule.id}</strong></span><span class="mono">${label("protocol", rule.protocol)} · ${rule.ports}</span></li>`;
}

function customRule(rule, index) {
  return html`<div class="custom-rule" data-custom="${index}">
    <label class="field"><span>${t("firewallName")}</span><input data-field="name" value="${rule.name || ""}" maxlength="100"></label>
    <label class="field"><span>${t("firewallProtocol")}</span><select data-field="protocol">${options("protocol", PROTOCOLS, rule.protocol || "TCP")}</select></label>
    <label class="field"><span>${t("firewallPorts")}</span><input data-field="ports" value="${rule.ports || ""}" inputmode="numeric" maxlength="11" placeholder="26900-26903" title="${t("firewallPortsHelp")}"></label>
    <label class="field"><span>${t("firewallProfile")}</span><select data-field="profile">${options("profile", PROFILES, rule.profile || "Any")}</select></label>
    <label class="field field-note"><span>${t("firewallNote")}</span><input data-field="note" value="${rule.note || ""}" maxlength="200"></label>
    <div class="custom-rule-footer">
      <label class="check"><input type="checkbox" data-field="enabled" ${rule.enabled !== false ? html`checked` : ""}>${t("firewallEnabled")}</label>
      <button type="button" class="button button-ghost button-sm" data-remove="${index}">${icon("trash")}${t("delete")}</button>
    </div>
  </div>`;
}

function changeItem(change) {
  const rule = change.rule || {};
  const target = t(rule.management ? "firewallTarget_management" : "firewallTarget_game");
  const effect = change.action === "remove" ? "remove" : rule.enabled ? "enable" : "disable";
  const tone = { add: "badge-success", modify: "badge-info", remove: "badge-danger" }[change.action] || "";
  return html`<li>
    <span class="badge ${tone}">${tOr(`firewallAction_${change.action}`, change.action)}</span>
    <div><strong>${rule.name || rule.id}</strong>
      <p class="subtle">${label("protocol", rule.protocol)} · ${rule.ports} · ${label("profile", rule.profile)}</p>
      <p class="subtle">${t(`firewallEffect_${effect}`, { target })}</p></div>
  </li>`;
}

export default {
  async render(ctx) {
    let data = await api.get("/api/firewall", { signal: ctx.signal });
    let custom = [...(data.settings?.custom || [])];

    const readCustom = page => [...page.querySelectorAll("[data-custom]")].map(row => ({
      name: row.querySelector('[data-field="name"]').value,
      protocol: row.querySelector('[data-field="protocol"]').value,
      ports: row.querySelector('[data-field="ports"]').value,
      profile: row.querySelector('[data-field="profile"]').value,
      note: row.querySelector('[data-field="note"]').value,
      enabled: row.querySelector('[data-field="enabled"]').checked,
    }));

    const paint = () => {
      const settings = data.settings || {};
      const game = (data.desired || []).filter(rule => !rule.management && !String(rule.id || "").includes("Custom-"));
      ctx.main.innerHTML = html`<div id="firewall-page">
        ${pageHeader(t("nav_firewall"), t("firewallHelp"))}
        <div class="stack-lg">
          ${data.canApply ? "" : callout("warning", "alert", t("firewallAdmin"))}
          <div class="grid grid-2">
            <section class="card">
              <div class="card-header"><h3>${icon("saves")}${t("firewallGame")}</h3><p>${t("firewallGameHelp")}</p></div>
              <div class="card-body">
                <label class="check"><input type="checkbox" id="game-tcp" ${settings.gameTCP !== false ? html`checked` : ""}>${t("firewallGameTCP")}</label>
                <label class="check"><input type="checkbox" id="game-udp" ${settings.gameUDP !== false ? html`checked` : ""}>${t("firewallGameUDP")}</label>
                ${game.length ? html`<ul class="rule-list">${game.map(ruleItem)}</ul>` : html`<p class="subtle">${t("firewallGameDisabled")}</p>`}
              </div>
            </section>
            <section class="card">
              <div class="card-header"><h3>${icon("alert")}${t("firewallManagement")}</h3><p id="management-help">${t("firewallManagementHelp")}</p></div>
              <div class="card-body">
                <label class="check"><input type="checkbox" id="dashboard" ${settings.dashboard ? html`checked` : ""}>${t("firewallDashboard")}</label>
                <label class="check"><input type="checkbox" id="telnet" ${settings.telnet ? html`checked` : ""}>${t("firewallTelnet")}</label>
                <label class="check"><input type="checkbox" id="risk" aria-describedby="management-help">${t("firewallRisk")}</label>
              </div>
            </section>
          </div>
          <section class="card">
            <div class="card-header"><h3>${t("firewallCustom")}</h3><button class="button button-secondary button-sm" id="add-rule">${icon("plus")}${t("firewallAddRule")}</button><p>${t("firewallCustomHelp")}</p></div>
            <div class="card-body">${custom.length ? custom.map(customRule) : emptyState("firewall", t("firewallCustomEmpty"))}</div>
          </section>
          <div class="button-group">
            <button class="button button-secondary" id="save-settings">${t("firewallSave")}</button>
            <button class="button button-primary" id="preview">${icon("firewall")}${t("firewallPreview")}</button>
          </div>
        </div>
        <dialog class="dialog dialog-wide" id="preview-dialog" aria-labelledby="preview-title">
          <div class="dialog-body">
            <h2 id="preview-title">${t("firewallDialog")}</h2>
            <p>${t("firewallDialogHelp")}</p>
            <div id="preview-changes"></div>
            <div class="dialog-actions">
              <button class="button button-secondary" id="preview-cancel">${t("cancel")}</button>
              <button class="button button-primary" id="apply" ${data.canApply ? "" : html`disabled`}>${t("firewallApply")}</button>
            </div>
          </div>
        </dialog>
      </div>`.value;
      bind();
    };

    const collect = page => ({
      gameTCP: page.querySelector("#game-tcp").checked,
      gameUDP: page.querySelector("#game-udp").checked,
      dashboard: page.querySelector("#dashboard").checked,
      telnet: page.querySelector("#telnet").checked,
      custom: readCustom(page),
    });

    const save = async page => {
      const settings = collect(page);
      if ((settings.dashboard || settings.telnet) && !page.querySelector("#risk").checked) {
        toast(t("firewallManagementRisk"), "error");
        page.querySelector("#risk").focus();
        return false;
      }
      data = { ...data, ...(await api.put("/api/firewall/settings", { settings })) };
      custom = [...(data.settings?.custom || [])];
      return true;
    };

    const bind = () => {
      const page = ctx.main.querySelector("#firewall-page");
      const keepRisk = () => page.querySelector("#risk").checked;
      page.querySelector("#add-rule").addEventListener("click", () => {
        custom = [...readCustom(page), { name: t("firewallCustomDefault"), protocol: "TCP", ports: "", profile: "Any", note: "", enabled: true }];
        const risk = keepRisk();
        const current = collect(page);
        data = { ...data, settings: { ...data.settings, ...current, custom } };
        paint();
        ctx.main.querySelector("#risk").checked = risk;
        ctx.main.querySelector('[data-custom]:last-of-type [data-field="ports"]')?.focus();
      });
      for (const button of page.querySelectorAll("[data-remove]")) {
        button.addEventListener("click", async () => {
          if (!(await confirmAction({ title: t("firewallDeleteTitle"), message: t("firewallDeleteConfirm"), confirmLabel: t("delete"), danger: true }))) return;
          const risk = keepRisk();
          const current = collect(page);
          current.custom.splice(Number(button.dataset.remove), 1);
          custom = current.custom;
          data = { ...data, settings: { ...data.settings, ...current } };
          paint();
          ctx.main.querySelector("#risk").checked = risk;
        });
      }
      page.querySelector("#save-settings").addEventListener("click", event => withBusy(event.currentTarget, t("firewallSaving"), async () => {
        if (await save(page)) {
          toast(t("firewallSaved"), "success");
          paint();
        }
      }));
      const dialog = page.querySelector("#preview-dialog");
      page.querySelector("#preview").addEventListener("click", event => withBusy(event.currentTarget, t("working"), async () => {
        if (!(await save(page))) return;
        data = await api.post("/api/firewall/preview");
        const changes = (data.changes || []).filter(change => change.action !== "unchanged");
        page.querySelector("#preview-changes").innerHTML = (changes.length
          ? html`<ul class="change-list">${changes.map(changeItem)}</ul>`
          : callout("success", "checkCircle", t("firewallNoChanges"))).value;
        page.querySelector("#apply").disabled = !data.canApply || !changes.length;
        dialog.showModal();
      }));
      page.querySelector("#preview-cancel").addEventListener("click", () => dialog.close());
      page.querySelector("#apply").addEventListener("click", event => withBusy(event.currentTarget, t("working"), async () => {
        data = await api.post("/api/firewall/apply", { hash: data.hash });
        custom = [...(data.settings?.custom || [])];
        dialog.close();
        toast(t("firewallApplied"), "success");
        paint();
      }));
    };

    paint();
  },
};
