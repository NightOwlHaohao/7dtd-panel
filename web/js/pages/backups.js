import { api } from "../api.js";
import { formatBytes, formatDateTime } from "../format.js";
import { html } from "../html.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { callout, confirmAction, emptyState, pageHeader, toast, withBusy } from "../ui.js";

function backupTable(list) {
  if (!list.length) return emptyState("backups", t("noBackups"), t("noBackupsHelp"));
  const sorted = [...list].sort((a, b) => new Date(b.created) - new Date(a.created));
  return html`<div class="table-wrap"><table class="table">
    <thead><tr><th>${t("backupName")}</th><th>${t("created")}</th><th class="num">${t("size")}</th><th><span class="visually-hidden">${t("actions")}</span></th></tr></thead>
    <tbody>${sorted.map(backup => html`<tr>
      <td class="mono">${backup.name} ${backup.auto ? html`<span class="badge badge-info">${t("backupAuto")}</span>` : ""}</td>
      <td class="nowrap">${formatDateTime(backup.created)}</td>
      <td class="num nowrap">${formatBytes(backup.size)}</td>
      <td class="nowrap"><div class="button-group">
        <button class="button button-secondary button-sm" data-restore="${backup.name}" data-requires="stopped">${icon("refresh")}${t("backupRestore")}</button>
        <button class="button button-ghost button-sm" data-delete="${backup.name}" aria-label="${t("delete")} ${backup.name}">${icon("trash")}</button>
      </div></td>
    </tr>`)}</tbody>
  </table></div>`;
}

export default {
  async render(ctx) {
    const list = await api.get("/api/backups", { signal: ctx.signal });
    ctx.main.innerHTML = html`<div id="backups-page">
      ${pageHeader(t("nav_backups"), t("backupsHelp"), html`
        <button class="button button-secondary" id="open-folder">${icon("folder")}${t("openFolder")}</button>
        <button class="button button-primary" id="create-backup">${icon("backups")}${t("createBackup")}</button>`)}
      <div class="stack">
        <div data-show-when="running" hidden>${callout("info", "info", t("backupsLive"))}</div>
        <div data-show-when="stopped" hidden>${callout("info", "info", t("backupsRestoreHelp"))}</div>
        <p class="subtle">${t("backupsScheduleHint")} <a href="#/schedule">${t("nav_schedule")}</a></p>
        <section class="card" id="backup-list">${backupTable(list)}</section>
      </div>
    </div>`.value;

    const page = ctx.main.querySelector("#backups-page");
    const refresh = async () => {
      page.querySelector("#backup-list").innerHTML = backupTable(await api.get("/api/backups")).value;
      bindRows();
      ctx.gates();
    };
    const bindRows = () => {
      for (const button of page.querySelectorAll("[data-restore]")) {
        button.addEventListener("click", async () => {
          const name = button.dataset.restore;
          if (!(await confirmAction({ title: t("backupRestoreTitle"), message: t("backupRestoreConfirm", { name }), confirmLabel: t("backupRestore"), danger: true }))) return;
          await withBusy(button, t("working"), async () => {
            const result = await api.post(`/api/backups/${encodeURIComponent(name)}/restore`, { confirm: true });
            toast(result.previous ? t("backupRestoredKept", { path: result.previous }) : t("backupRestored"), "success");
          });
        });
      }
      for (const button of page.querySelectorAll("[data-delete]")) {
        button.addEventListener("click", async () => {
          const name = button.dataset.delete;
          if (!(await confirmAction({ title: t("backupDeleteTitle"), message: t("backupDeleteConfirm", { name }), confirmLabel: t("delete"), danger: true }))) return;
          await withBusy(button, "", async () => {
            await api.delete(`/api/backups/${encodeURIComponent(name)}`);
            toast(t("backupDeleted"), "success");
            await refresh();
          });
        });
      }
    };
    bindRows();

    page.querySelector("#create-backup").addEventListener("click", event => withBusy(event.currentTarget, t("working"), async () => {
      const backup = await api.post("/api/backups");
      toast(t("backupCreated", { name: backup.name }), "success");
      await refresh();
    }));
    page.querySelector("#open-folder").addEventListener("click", event => withBusy(event.currentTarget, t("working"), async () => {
      const result = await api.post("/api/backups/open-folder");
      if (result.folder) toast(t("backupFolderAt", { path: result.folder }), "info");
    }));
    ctx.on("activity", entry => {
      if (entry.source === "backup") refresh().catch(() => {});
    });
  },
};
