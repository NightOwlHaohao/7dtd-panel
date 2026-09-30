import { api } from "../api.js";
import { formatDateTime } from "../format.js";
import { html } from "../html.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { appendLogLines, logWriter } from "../logs.js";
import { bindServerControls, serverControls } from "../server-controls.js";
import { state } from "../store.js";
import { callout, confirmAction, pageHeader, stateBadge, toast, withBusy } from "../ui.js";

function controlsCard(status) {
  return html`<section class="card">
    <div class="card-header"><h3>${t("serverControlsTitle")}</h3>${stateBadge(status)}<p>${t("serverControlsHelp")}</p></div>
    <div class="card-body">
      ${serverControls()}
      ${status.pid ? html`<p class="subtle">${t("pidLabel")}: <span class="mono">${status.pid}</span></p>` : ""}
      ${status.diagnostic ? callout("warning", "alert", html`<strong>${t("diagnostic")}</strong>${status.diagnostic}`) : ""}
      ${status.lastCrash ? callout("danger", "alert", t("lastCrashNotice", { time: formatDateTime(status.lastCrash) })) : ""}
    </div>
  </section>`;
}

function updatesCard(update, setup) {
  const target = update.target ? t(`updateTarget_${update.target}`) : "";
  const running = update.state === "running";
  const line = update.state && update.state !== "idle" && target ? t("updateStatusLine", { target, state: t(`update_${update.state}`) }) : "";
  return html`<section class="card">
    <div class="card-header"><h3>${t("updatesTitle")}</h3>
      <span class="badge ${setup.steamCmd ? "badge-success" : ""}">${t("steamcmdStatus")}: ${setup.steamCmd ? t("steamInstalled") : t("steamNotInstalled")}</span>
      <p>${t("updatesHelp")}</p></div>
    <div class="card-body">
      ${line ? html`<p class="subtle" aria-live="polite">${line}</p>` : ""}
      ${update.state === "error" ? callout("danger", "alert", t("updateFailed", { target, error: update.lastError || t("update_error") })) : ""}
      <ul class="action-list">
        <li><div><strong>${t("updateServer")}</strong><p class="subtle">${t("updateServerHelp")}</p></div>
          <button class="button button-primary" data-update="server" data-requires="stopped" data-locked="${running}">${icon("download")}${t("updateServer")}</button></li>
        <li><div><strong>${t("updateSteamCMD")}</strong><p class="subtle">${t("updateSteamCMDHelp")}</p></div>
          <button class="button button-secondary" data-update="steamcmd" data-requires="stopped" data-locked="${running}">${icon("refresh")}${t("updateSteamCMD")}</button></li>
      </ul>
    </div>
  </section>`;
}

const KNOWN_BRANCHES = ["", "public", "latest_experimental"];

function branchCard(versions) {
  const game = versions.game;
  const steam = versions.steam;
  const custom = !KNOWN_BRANCHES.includes(steam.branch);
  const branchLabel = name => (name === "" ? t("branchDefault") : name === "public" ? t("branchPublic") : name === "latest_experimental" ? t("branchExperimental") : name);
  return html`<section class="card">
    <div class="card-header"><h3>${t("gameVersionTitle")}</h3>
      ${game.updateAvailable ? html`<span class="badge badge-accent">${t("gameUpdateAvailable", { build: game.latest })}</span>` : ""}
      <p>${t("gameVersionHelp")}</p></div>
    <div class="card-body stack">
      <dl class="kv">
        <div><dt>${t("gameInstalledBuild")}</dt><dd class="mono">${game.installed.installed ? `${game.installed.buildId || "?"} (${game.installed.branch})` : t("gameNotInstalled")}</dd></div>
        <div><dt>${t("gameLatestBuild")}</dt><dd class="mono">${game.latest ? `${game.latest} (${game.branch})` : "—"}</dd></div>
        <div><dt>${t("lastChecked")}</dt><dd>${game.checkedAt ? formatDateTime(game.checkedAt) : t("neverChecked")}</dd></div>
      </dl>
      ${game.error ? callout("warning", "alert", t("checkFailed", { error: game.error })) : ""}
      <form class="stack" id="branch-form">
        <div class="row">
          <label class="field"><span>${t("branchLabel")}</span><select id="branch-select">
            ${KNOWN_BRANCHES.map(name => html`<option value="${name}" ${!custom && steam.branch === name ? html`selected` : ""}>${branchLabel(name)}</option>`)}
            <option value="custom" ${custom ? html`selected` : ""}>${t("branchCustom")}</option></select></label>
          <label class="field field-grow" id="branch-custom-field" ${custom ? "" : html`hidden`}><span>${t("branchName")}</span>
            <input id="branch-custom" value="${custom ? steam.branch : ""}" list="branch-names" maxlength="64" spellcheck="false" placeholder="alpha21.2"></label>
          <label class="field" id="branch-password-field" ${custom ? "" : html`hidden`}><span>${t("branchPassword")}</span>
            <input id="branch-password" type="password" autocomplete="off" maxlength="128" placeholder="${steam.hasPassword ? t("branchPasswordKept") : ""}"></label>
        </div>
        <datalist id="branch-names">${(game.branches || []).map(branch => html`<option value="${branch.name}">${branch.buildId}${branch.password ? " 🔒" : ""}</option>`)}</datalist>
        <label class="check"><input type="checkbox" id="auto-check" ${steam.autoCheck ? html`checked` : ""}>${t("autoCheckUpdates")}</label>
        <p class="field-hint">${t("branchHelp")}</p>
        <div class="button-group">
          <button class="button button-secondary">${t("save")}</button>
          <button type="button" class="button button-ghost" id="check-game">${icon("refresh")}${t("checkGameNow")}</button>
        </div>
      </form>
    </div>
  </section>`;
}

function commandCard() {
  return html`<section class="card">
    <div class="card-header"><h3>${t("commandTitle")}</h3><p>${t("commandHelp")}</p></div>
    <form class="card-body" id="command-form">
      <div class="row">
        <label class="field field-grow"><span class="visually-hidden">${t("commandLabel")}</span>
          <input id="command" autocomplete="off" spellcheck="false" maxlength="500" data-requires="running"></label>
        <button class="button button-secondary" data-requires="running">${t("send")}</button>
      </div>
    </form>
  </section>`;
}

export default {
  async render(ctx) {
    const [update, setup, versions] = await Promise.all([
      api.get("/api/update", { signal: ctx.signal }),
      api.get("/api/setup", { signal: ctx.signal }),
      api.get("/api/versions", { signal: ctx.signal }),
    ]);
    ctx.main.innerHTML = html`${pageHeader(t("nav_server"), "")}
      <div class="stack-lg">
        <div class="grid grid-2">
          <div id="controls">${controlsCard(state.status)}</div>
          <div id="updates">${updatesCard(update, setup)}</div>
        </div>
        <section class="card">
          <div class="card-header"><h3>${t("serverLogTitle")}</h3>
            <label class="check"><input type="checkbox" id="follow" checked> ${t("autoScroll")}</label>
            <p>${t("serverLogHelp")}</p></div>
          <pre class="log log-panel" id="server-log" tabindex="0"><span class="log-empty subtle">${t("logEmpty")}</span></pre>
        </section>
        <div id="branch">${branchCard(versions)}</div>
        ${commandCard()}
        ${callout("info", "info", html`<strong>${t("panelServiceTitle")}</strong>${t("panelServiceGuide")} <a href="#/panel">${t("nav_panel")}</a>`)}
      </div>`.value;

    const controls = ctx.main.querySelector("#controls");
    bindServerControls(controls);
    let controlsTimer;
    const refreshControls = () => {
      clearTimeout(controlsTimer);
      if (controls.querySelector("[aria-busy='true']")) {
        controlsTimer = setTimeout(refreshControls, 150); // wait for the running action
        return;
      }
      controls.innerHTML = controlsCard(state.status).value;
      bindServerControls(controls);
    };
    ctx.on("status", refreshControls);
    ctx.onCleanup(() => clearTimeout(controlsTimer));

    const bindUpdates = () => {
      for (const button of ctx.main.querySelectorAll("[data-update]")) {
        button.onclick = async () => {
          const target = button.dataset.update;
          const ok = await confirmAction({
            title: t(target === "server" ? "confirmUpdateServerTitle" : "confirmUpdateSteamCMDTitle"),
            message: t(target === "server" ? "confirmUpdateServer" : "confirmUpdateSteamCMD"),
            confirmLabel: t(target === "server" ? "updateServer" : "updateSteamCMD"),
          });
          if (!ok) return;
          await withBusy(button, t("working"), async () => {
            await api.post(`/api/update/${target}`);
            toast(t("updateStarted"), "info");
            await refreshUpdates();
          });
        };
      }
      ctx.gates();
    };
    const refreshUpdates = async () => {
      const [next, nextSetup] = await Promise.all([api.get("/api/update"), api.get("/api/setup")]);
      if (!ctx.isCurrent()) return;
      ctx.main.querySelector("#updates").innerHTML = updatesCard(next, nextSetup).value;
      bindUpdates();
    };
    bindUpdates();
    ctx.on("activity", entry => {
      if (entry.source === "update") refreshUpdates().catch(() => {});
    });

    const bindBranch = () => {
      const root = ctx.main.querySelector("#branch");
      const select = root.querySelector("#branch-select");
      select.addEventListener("change", () => {
        const custom = select.value === "custom";
        root.querySelector("#branch-custom-field").hidden = !custom;
        root.querySelector("#branch-password-field").hidden = !custom;
      });
      const paintBranch = next => {
        root.innerHTML = branchCard(next).value;
        bindBranch();
      };
      root.querySelector("#branch-form").addEventListener("submit", event => {
        event.preventDefault();
        const custom = select.value === "custom";
        const branch = custom ? root.querySelector("#branch-custom").value.trim() : select.value;
        const password = custom ? root.querySelector("#branch-password").value : "";
        withBusy(event.submitter, t("working"), async () => {
          paintBranch(await api.put("/api/versions/settings", { branch, password, keepPassword: custom && !password, autoCheck: root.querySelector("#auto-check").checked }));
          toast(t("saved"), "success");
        });
      });
      root.querySelector("#check-game").addEventListener("click", event => withBusy(event.currentTarget, t("working"), async () => {
        paintBranch(await api.post("/api/versions/check", { target: "game" }));
      }));
    };
    bindBranch();

    const log = ctx.main.querySelector("#server-log");
    const follow = ctx.main.querySelector("#follow");
    appendLogLines(log, state.logLines, { follow: true });
    ctx.on("log", logWriter(log, () => ({ follow: follow.checked })));

    ctx.main.querySelector("#command-form").addEventListener("submit", event => {
      event.preventDefault();
      const input = ctx.main.querySelector("#command");
      const command = input.value.trim();
      if (!command) return;
      withBusy(event.submitter || event.target.querySelector("button"), t("sending"), async () => {
        await api.post("/api/server/console", { command });
        input.value = "";
        toast(t("commandSent"), "success");
      });
    });
  },
};
