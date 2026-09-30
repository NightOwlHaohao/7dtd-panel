import { api } from "../api.js";
import { formatDateTime } from "../format.js";
import { html } from "../html.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { callout, confirmAction, copyText, emptyState, pageHeader, toast, withBusy } from "../ui.js";

const BAN_UNITS = ["minutes", "hours", "days", "weeks", "months", "years"];

function onlineTable(data) {
  if (!data.running) return emptyState("users", t("playersServerStopped"));
  if (data.onlineError) return callout("warning", "alert", t("playersOnlineError", { error: data.onlineError }));
  if (!data.online.length) return emptyState("users", t("playersNoneOnline"));
  return html`<div class="table-wrap"><table class="table">
    <thead><tr><th>${t("playerName")}</th><th>${t("playerId")}</th><th class="num">${t("playerLevel")}</th><th class="num">${t("playerPing")}</th><th>${t("playerIp")}</th><th><span class="visually-hidden">${t("actions")}</span></th></tr></thead>
    <tbody>${data.online.map(player => html`<tr>
      <td><strong>${player.name}</strong><div class="subtle">${t("playerStats", { health: player.health, zombies: player.zombies, deaths: player.deaths })}</div></td>
      <td class="mono"><button class="button button-ghost button-sm" data-copy="${player.platformId}" title="${t("copy")}">${player.platformId || "—"}</button><div class="subtle">#${player.entityId}</div></td>
      <td class="num">${player.level}</td>
      <td class="num">${player.ping}</td>
      <td class="mono">${player.ip || "—"}</td>
      <td class="nowrap"><div class="button-group">
        <button class="button button-secondary button-sm" data-act="pm" data-target="${player.entityId}" data-name="${player.name}">${t("playerMessage")}</button>
        <button class="button button-secondary button-sm" data-act="kick" data-target="${player.entityId}" data-name="${player.name}">${t("playerKick")}</button>
        <button class="button button-danger-outline button-sm" data-act="ban" data-target="${player.platformId || player.entityId}" data-name="${player.name}">${t("playerBan")}</button>
      </div></td>
    </tr>`)}</tbody>
  </table></div>`;
}

function entryList(entries, removeAction, extra) {
  if (!entries.length) return html`<p class="subtle">${t("none")}</p>`;
  return html`<ul class="rule-list">${entries.map(entry => html`<li>
    <span><strong>${entry.name || entry.id}</strong> <span class="mono subtle">${entry.id}</span>${extra ? extra(entry) : ""}</span>
    <button class="button button-ghost button-sm" data-act="${removeAction}" data-target="${entry.id}" data-name="${entry.name || entry.id}" data-requires="running">${icon("x")}${t("remove")}</button>
  </li>`)}</ul>`;
}

function listsCard(lists) {
  return html`<section class="card">
    <div class="card-header"><h3>${t("playersLists")}</h3><p>${t("playersListsHelp")} <span class="mono">${lists.file}</span></p></div>
    <div class="card-body stack">
      ${lists.error ? callout("warning", "alert", lists.error) : ""}
      ${!lists.found ? html`<p class="subtle">${t("playersAdminFileMissing")}</p>` : ""}
      <div><h4>${t("playersAdmins")}</h4>${entryList(lists.admins, "admin_remove", entry => html` <span class="badge">${t("playerPermission", { level: entry.permission })}</span>`)}</div>
      <div><h4>${t("playersWhitelist")}</h4>${entryList(lists.whitelist, "whitelist_remove")}</div>
      <div><h4>${t("playersBanned")}</h4>${entryList(lists.banned, "unban", entry => html`${entry.until ? html` <span class="badge badge-danger">${t("playerBannedUntil", { time: entry.until })}</span>` : ""}${entry.reason ? html` <span class="subtle">${entry.reason}</span>` : ""}`)}</div>
    </div>
  </section>`;
}

function manageCard() {
  return html`<section class="card">
    <div class="card-header"><h3>${t("playersManage")}</h3><p>${t("playersManageHelp")}</p></div>
    <form class="card-body stack" id="manage-form">
      <div class="row">
        <label class="field field-grow"><span>${t("playerTarget")}</span><input id="manage-target" list="known-ids" maxlength="64" spellcheck="false" autocomplete="off" placeholder="Steam_7656… / EOS_… / 171"></label>
        <label class="field"><span>${t("playerAdminLevel")}</span><input id="manage-level" type="number" min="0" max="1000" value="0"></label>
      </div>
      <div class="button-group">
        <button type="button" class="button button-secondary" data-manage="whitelist_add" data-requires="running">${t("playerWhitelistAdd")}</button>
        <button type="button" class="button button-secondary" data-manage="admin_add" data-requires="running">${t("playerAdminAdd")}</button>
        <button type="button" class="button button-danger-outline" data-manage="ban" data-requires="running">${t("playerBan")}</button>
      </div>
      <label class="field"><span>${t("playersBroadcast")}</span>
        <div class="row"><input id="broadcast" class="field-grow" maxlength="300" data-requires="running"><button type="button" class="button button-primary" id="broadcast-send" data-requires="running">${t("send")}</button></div></label>
    </form>
  </section>`;
}

function knownCard(known) {
  return html`<section class="card">
    <div class="card-header"><h3>${t("playersKnown")}</h3><p>${t("playersKnownHelp")}</p></div>
    ${known.length ? html`<div class="table-wrap"><table class="table">
      <thead><tr><th>${t("playerName")}</th><th>${t("playerId")}</th><th>${t("playerLastSeen")}</th><th>${t("playerIp")}</th></tr></thead>
      <tbody>${known.slice(0, 200).map(player => html`<tr>
        <td>${player.name}</td>
        <td class="mono"><button class="button button-ghost button-sm" data-copy="${player.platformId}" title="${t("copy")}">${player.platformId}</button></td>
        <td class="nowrap">${formatDateTime(player.lastSeen)}</td>
        <td class="mono">${player.ip || "—"}</td></tr>`)}</tbody>
    </table></div>` : html`<div class="card-body">${emptyState("users", t("playersKnownEmpty"))}</div>`}
    <datalist id="known-ids">${known.map(player => html`<option value="${player.platformId}">${player.name}</option>`)}</datalist>
  </section>`;
}

function naiwaziCard(n) {
  const status = !n.installed ? html`<span class="badge">${t("naiwaziNotInstalled")}</span>`
    : !n.enabled ? html`<span class="badge badge-warning">${t("modDisabled")}</span>`
    : n.listening ? html`<span class="badge badge-success">${t("naiwaziListening")}</span>`
    : html`<span class="badge">${t("naiwaziNotListening")}</span>`;
  return html`<section class="card" id="naiwazi">
    <div class="card-header"><h3>NaiwaziBot</h3>${status}<p>${t("naiwaziHelp")}</p></div>
    <div class="card-body stack">
      ${n.installed ? html`<dl class="kv">
        <div><dt>${t("naiwaziVersion")}</dt><dd>${n.version || t("unknown")}</dd></div>
        <div><dt>${t("naiwaziPort")}</dt><dd class="mono">${n.port}${n.portFromGame ? html` <span class="subtle">${t("naiwaziPortDefault")}</span>` : ""}</dd></div>
        <div><dt>${t("naiwaziPasswordFile")}</dt><dd class="mono">${n.passwordFile || t("naiwaziPasswordPending")}</dd></div>
      </dl>
      <div class="button-group">
        ${n.url ? html`<a class="button button-primary" href="${n.url}" target="_blank" rel="noopener noreferrer">${icon("upload")}${t("naiwaziOpen")}</a>` : ""}
        <button class="button button-secondary" id="naiwazi-password" ${n.passwordFile ? "" : html`disabled`}>${t("naiwaziShowPassword")}</button>
        <a class="button button-ghost" href="#/firewall">${icon("firewall")}${t("naiwaziFirewall", { port: n.port })}</a>
      </div>
      <pre class="log log-short" id="naiwazi-password-text" hidden></pre>
      <form class="row" id="naiwazi-port-form">
        <label class="field"><span>${t("naiwaziPortOverride")}</span><input id="naiwazi-port" type="number" min="0" max="65535" value="${n.portFromGame ? 0 : n.port}"></label>
        <button class="button button-secondary">${t("save")}</button>
      </form>
      ${callout("warning", "alert", t("naiwaziExposure"))}`
      : html`<p>${t("naiwaziInstallHint")}</p><div class="button-group"><a class="button button-secondary" href="#/mods">${icon("mods")}${t("nav_mods")}</a></div>`}
      <p class="subtle">${t("naiwaziLicense")} <a href="http://cn.naiwazi.com/bot/" target="_blank" rel="noopener noreferrer">${t("naiwaziDocs")}</a></p>
    </div>
  </section>`;
}

async function promptAction(action, target, name) {
  const dialog = document.querySelector("#player-dialog");
  const form = dialog.querySelector("form");
  dialog.querySelector("#player-dialog-title").textContent = t(`playerActionTitle_${action}`, { name });
  for (const node of form.querySelectorAll("[data-for]")) node.hidden = !node.dataset.for.split(" ").includes(action);
  form.reset();
  dialog.showModal();
  return new Promise(resolve => {
    dialog.addEventListener("close", () => {
      if (dialog.returnValue !== "confirm") return resolve(null);
      resolve({
        action, target,
        reason: form.querySelector("#pd-reason").value,
        message: form.querySelector("#pd-message").value,
        duration: Number(form.querySelector("#pd-duration").value),
        unit: form.querySelector("#pd-unit").value,
      });
    }, { once: true });
  });
}

const playerDialog = () => html`<dialog class="dialog" id="player-dialog" aria-labelledby="player-dialog-title">
  <form method="dialog" class="stack">
    <h2 id="player-dialog-title"></h2>
    <label class="field" data-for="pm"><span>${t("playerMessageText")}</span><input id="pd-message" maxlength="300"></label>
    <label class="field" data-for="kick ban"><span>${t("playerReason")}</span><input id="pd-reason" maxlength="200"></label>
    <div class="row" data-for="ban">
      <label class="field"><span>${t("playerBanDuration")}</span><input id="pd-duration" type="number" min="1" max="100000" value="1"></label>
      <label class="field"><span>${t("playerBanUnit")}</span><select id="pd-unit">${BAN_UNITS.map(unit => html`<option value="${unit}" ${unit === "days" ? html`selected` : ""}>${t(`unit_${unit}`)}</option>`)}</select></label>
    </div>
    <div class="dialog-actions">
      <button class="button button-secondary" value="cancel">${t("cancel")}</button>
      <button class="button button-primary" value="confirm">${t("confirm")}</button>
    </div>
  </form>
</dialog>`;

export default {
  async render(ctx) {
    let data = await api.get("/api/players", { signal: ctx.signal });

    const run = async (button, body, done) => withBusy(button, t("working"), async () => {
      await api.post("/api/players/action", body);
      toast(done, "success");
      await reload();
    });

    const paint = () => {
      ctx.main.innerHTML = html`<div id="players-page">
        ${pageHeader(t("nav_players"), t("playersHelp"), html`<button class="button button-secondary" id="players-refresh">${icon("refresh")}${t("refresh")}</button>`)}
        <div class="stack-lg">
          <section class="card"><div class="card-header"><h3>${t("playersOnline", { count: data.online.length })}</h3></div>${onlineTable(data)}</section>
          <div class="grid grid-2">${manageCard()}${listsCard(data.lists)}</div>
          ${naiwaziCard(data.naiwazi)}
          ${knownCard(data.known)}
        </div>
        ${playerDialog()}
      </div>`.value;
      bind();
      ctx.gates();
    };

    const reload = async () => {
      data = await api.get("/api/players");
      if (ctx.isCurrent()) paint();
    };

    const bind = () => {
      const page = ctx.main.querySelector("#players-page");
      page.querySelector("#players-refresh").addEventListener("click", event => withBusy(event.currentTarget, t("working"), reload));
      for (const button of page.querySelectorAll("[data-copy]")) button.addEventListener("click", () => button.dataset.copy && copyText(button.dataset.copy));
      for (const button of page.querySelectorAll("[data-act]")) {
        button.addEventListener("click", async () => {
          const { act, target, name } = button.dataset;
          if (["pm", "kick", "ban"].includes(act)) {
            const body = await promptAction(act, target, name);
            if (body) await run(button, body, t("playerActionDone"));
            return;
          }
          if (!(await confirmAction({ title: t(`playerActionTitle_${act}`, { name }), message: target, confirmLabel: t("confirm"), danger: act !== "unban" }))) return;
          await run(button, { action: act, target }, t("playerActionDone"));
        });
      }
      const targetInput = page.querySelector("#manage-target");
      for (const button of page.querySelectorAll("[data-manage]")) {
        button.addEventListener("click", async () => {
          const target = targetInput.value.trim();
          if (!target) { toast(t("playerTargetRequired"), "error"); targetInput.focus(); return; }
          const action = button.dataset.manage;
          if (action === "ban") {
            const body = await promptAction("ban", target, target);
            if (body) await run(button, body, t("playerActionDone"));
            return;
          }
          await run(button, { action, target, level: Number(page.querySelector("#manage-level").value) }, t("playerActionDone"));
        });
      }
      page.querySelector("#broadcast-send").addEventListener("click", event => {
        const message = page.querySelector("#broadcast").value.trim();
        if (message) run(event.currentTarget, { action: "say", message }, t("commandSent"));
      });
      page.querySelector("#naiwazi-password")?.addEventListener("click", event => withBusy(event.currentTarget, t("working"), async () => {
        const result = await api.post("/api/naiwazi/password");
        const pre = page.querySelector("#naiwazi-password-text");
        pre.textContent = result.text;
        pre.hidden = false;
      }));
      page.querySelector("#naiwazi-port-form")?.addEventListener("submit", event => {
        event.preventDefault();
        withBusy(event.submitter, t("working"), async () => {
          data.naiwazi = await api.put("/api/naiwazi/port", { port: Number(page.querySelector("#naiwazi-port").value) || 0 });
          toast(t("saved"), "success");
          paint();
        });
      });
    };

    paint();
    ctx.on("status", status => {
      if ((status.state === "running") !== Boolean(data.running)) reload().catch(() => {});
    });
  },
};
