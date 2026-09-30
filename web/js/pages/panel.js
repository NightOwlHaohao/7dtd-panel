import { api } from "../api.js";
import { closeEvents } from "../events.js";
import { formatDateTime } from "../format.js";
import { html } from "../html.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { callout, confirmAction, pageHeader, toast, withBusy } from "../ui.js";

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

/** Wait until a panel answers again (the service after a hand-over), then reload. */
async function reloadWhenBack(signal) {
  await sleep(1500);
  for (let attempt = 0; attempt < 60 && !signal?.aborted; attempt++) {
    try {
      const response = await fetch("/api/session", { cache: "no-store" });
      if (response.ok) { location.reload(); return; }
    } catch { /* not up yet */ }
    await sleep(1000);
  }
  toast(t("panelSwitchTimeout"), "error");
}

function stoppedScreen() {
  closeEvents();
  document.body.innerHTML = html`<main class="main"><div class="empty">${icon("power")}<strong>${t("panelStoppedTitle")}</strong><p>${t("panelStoppedHelp")}</p></div></main>`.value;
}

function serviceCard(info) {
  const service = info.service;
  if (!service.supported) {
    return html`<section class="card"><div class="card-header"><h3>${t("serviceTitle")}</h3></div><div class="card-body">${callout("info", "info", t("serviceUnsupported"))}</div></section>`;
  }
  const stateBadge = service.installed
    ? html`<span class="badge ${service.state === "running" ? "badge-success" : ""}">${t(`serviceState_${service.state || "stopped"}`)}</span>`
    : html`<span class="badge">${t("serviceNotInstalled")}</span>`;
  return html`<section class="card">
    <div class="card-header"><h3>${t("serviceTitle")}</h3>${stateBadge}<p>${t("serviceHelp")}</p></div>
    <div class="card-body stack">
      ${service.error ? callout("warning", "alert", service.error) : ""}
      ${service.installed && !service.currentExe ? callout("warning", "alert", t("serviceOtherExe", { path: service.exePath || t("unknown") })) : ""}
      <dl class="kv">
        <div><dt>${t("serviceName")}</dt><dd class="mono">${service.name}</dd></div>
        <div><dt>${t("serviceAccount")}</dt><dd class="mono">${service.account}</dd></div>
        ${service.installed ? html`<div><dt>${t("serviceExe")}</dt><dd class="mono">${service.exePath}</dd></div>` : ""}
      </dl>
      ${service.installed ? html`<label class="check"><input type="checkbox" id="autostart" ${service.autoStart ? html`checked` : ""} ${info.runningAsService ? html`disabled` : ""}>${t("serviceAutoStart")}</label>
        <p class="field-hint">${info.runningAsService ? html`${t("serviceAutoStartFromConsole")} <code class="mono">panel.exe service autostart ${service.autoStart ? "off" : "on"}</code>` : t("serviceAutoStartHelp")}</p>` : ""}
      <div class="button-group">
        ${!info.runningAsService ? html`<button class="button button-primary" id="service-install">${icon("download")}${t(service.installed ? "serviceUpdate" : "serviceInstall")}</button>` : ""}
        ${service.installed ? html`<button class="button button-danger-outline" id="service-uninstall">${icon("trash")}${t("serviceUninstall")}</button>` : ""}
      </div>
      <p class="field-hint">${t("serviceUacHint")}</p>
    </div>
  </section>`;
}

function versionCard(panel) {
  return html`<section class="card">
    <div class="card-header"><h3>${t("panelUpdateTitle")}</h3>
      ${panel.updateAvailable ? html`<span class="badge badge-accent">${t("updateAvailable", { version: panel.latest })}</span>` : ""}</div>
    <div class="card-body stack">
      <dl class="kv">
        <div><dt>${t("panelVersion")}</dt><dd class="mono">${panel.current}</dd></div>
        <div><dt>${t("panelLatest")}</dt><dd class="mono">${panel.latest || "—"}</dd></div>
        <div><dt>${t("lastChecked")}</dt><dd>${panel.checkedAt ? formatDateTime(panel.checkedAt) : t("neverChecked")}</dd></div>
      </dl>
      ${panel.error ? callout("warning", "alert", t("checkFailed", { error: panel.error })) : ""}
      ${panel.updateAvailable ? callout("info", "info", t("panelUpdateHowTo")) : ""}
      <div class="button-group">
        <button class="button button-secondary" id="check-panel">${icon("refresh")}${t("checkNow")}</button>
        ${panel.url ? html`<a class="button button-ghost" href="${panel.url}" target="_blank" rel="noopener noreferrer">${t("openRelease")}</a>` : ""}
      </div>
    </div>
  </section>`;
}

export default {
  async render(ctx) {
    let [info, versions] = await Promise.all([api.get("/api/panel", { signal: ctx.signal }), api.get("/api/versions", { signal: ctx.signal })]);

    const paint = () => {
      ctx.main.innerHTML = html`<div id="panel-page">
        ${pageHeader(t("nav_panel"), t("panelHelp"))}
        <div class="stack-lg">
          <section class="card">
            <div class="card-header"><h3>${t("panelRunTitle")}</h3>
              <span class="badge badge-info">${t(info.runningAsService ? "panelModeService" : "panelModeConsole")}</span>
              ${info.elevated ? html`<span class="badge badge-warning">${t("panelElevated")}</span>` : ""}</div>
            <div class="card-body stack">
              <dl class="kv">
                <div><dt>${t("panelVersion")}</dt><dd class="mono">${info.version}</dd></div>
                <div><dt>${t("panelAddress")}</dt><dd class="mono">http://${info.listen}</dd></div>
                <div><dt>${t("panelFolder")}</dt><dd class="mono">${info.root}</dd></div>
                <div><dt>${t("panelExe")}</dt><dd class="mono">${info.executable}</dd></div>
              </dl>
              ${callout("info", "info", html`<strong>${t("panelServiceTitle")}</strong>${t("panelStopGuide")}`)}
              <div class="button-group"><button class="button button-danger-outline" id="panel-stop">${icon("power")}${t("panelStop")}</button></div>
            </div>
          </section>
          <div class="grid grid-2">${serviceCard(info)}${versionCard(versions.panel)}</div>
        </div>
      </div>`.value;
      bind();
    };

    const refresh = async () => {
      [info, versions] = await Promise.all([api.get("/api/panel"), api.get("/api/versions")]);
      if (ctx.isCurrent()) paint();
    };

    const bind = () => {
      const page = ctx.main.querySelector("#panel-page");
      page.querySelector("#panel-stop").addEventListener("click", async event => {
        if (!(await confirmAction({ title: t("panelStopTitle"), message: t("panelStopConfirm"), confirmLabel: t("panelStop"), danger: true }))) return;
        await withBusy(event.currentTarget, t("working"), async () => {
          await api.post("/api/panel/stop");
          stoppedScreen();
        });
      });
      page.querySelector("#service-install")?.addEventListener("click", async event => {
        const update = info.service.installed;
        if (!(await confirmAction({ title: t(update ? "serviceUpdate" : "serviceInstall"), message: t("serviceInstallConfirm"), confirmLabel: t(update ? "serviceUpdate" : "serviceInstall") }))) return;
        await withBusy(event.currentTarget, t("serviceWaitingUac"), async () => {
          await api.post("/api/panel/service/install");
          toast(t("serviceSwitching"), "info");
          await reloadWhenBack(ctx.signal);
        });
      });
      page.querySelector("#service-uninstall")?.addEventListener("click", async event => {
        if (!(await confirmAction({ title: t("serviceUninstall"), message: t(info.runningAsService ? "serviceUninstallSelfConfirm" : "serviceUninstallConfirm"), confirmLabel: t("serviceUninstall"), danger: true }))) return;
        await withBusy(event.currentTarget, t("serviceWaitingUac"), async () => {
          const result = await api.post("/api/panel/service/uninstall");
          if (result.stopping) { stoppedScreen(); return; }
          toast(t("serviceUninstalled"), "success");
          await refresh();
        });
      });
      page.querySelector("#autostart")?.addEventListener("change", async event => {
        const box = event.currentTarget;
        const wanted = box.checked;
        box.disabled = true;
        try {
          info.service = await api.put("/api/panel/service/autostart", { enabled: wanted });
          toast(t("saved"), "success");
        } catch (error) {
          box.checked = !wanted;
          toast(error.message, "error");
        } finally {
          box.disabled = false;
        }
      });
      page.querySelector("#check-panel").addEventListener("click", event => withBusy(event.currentTarget, t("working"), async () => {
        versions = await api.post("/api/versions/check", { target: "panel" });
        paint();
      }));
    };

    paint();
  },
};
