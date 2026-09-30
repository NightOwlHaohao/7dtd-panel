import { api } from "../api.js";
import { formatBytes, formatDateTime } from "../format.js";
import { html } from "../html.js";
import { t, tOr } from "../i18n.js";
import { icon } from "../icons.js";
import { callout, confirmAction, emptyState, pageHeader, toast, withBusy } from "../ui.js";

let selectedWorld = "";

const kindLabel = world => tOr(`worldKind_${world.kind}`, world.kind);

function worldList(worlds) {
  if (!worlds.length) return emptyState("saves", t("noWorlds"));
  return html`<ul class="world-list">${worlds.map(world => html`<li>
    <button class="world-button" data-world="${world.name}" aria-current="${world.name === selectedWorld}">
      <span>${world.name}</span>
      <span class="badge ${world.missing ? "badge-danger" : ""}">${world.missing ? t("worldKind_missing") : kindLabel(world)}</span>
    </button></li>`)}</ul>`;
}

function saveTable(world) {
  if (!world) return emptyState("saves", t("selectWorld"));
  const saves = world.saves || [];
  if (!saves.length) return emptyState("saves", t("noSaves"));
  return html`<div class="table-wrap"><table class="table">
    <thead><tr><th>${t("saveName")}</th><th>${t("modified")}</th><th class="num">${t("size")}</th><th class="actions-cell">${t("actions")}</th></tr></thead>
    <tbody>${saves.map(save => html`<tr>
      <td><strong>${save.name}</strong> ${save.active ? html`<span class="badge badge-accent">${t("active")}</span>` : ""}</td>
      <td class="nowrap">${formatDateTime(save.modified)}</td>
      <td class="num nowrap">${formatBytes(save.size)}</td>
      <td class="actions-cell"><div class="button-group">
        <button class="button button-secondary button-sm" data-action="switch" data-world="${save.world}" data-game="${save.name}" data-requires="stopped" data-locked="${save.active}">${t("switch")}</button>
        <button class="button button-secondary button-sm" data-action="export" data-world="${save.world}" data-game="${save.name}" data-requires="stopped">${icon("download")}${t("export")}</button>
        <button class="button button-danger-outline button-sm" data-action="delete" data-world="${save.world}" data-game="${save.name}" data-requires="stopped">${icon("trash")}${t("delete")}</button>
      </div></td>
    </tr>`)}</tbody>
  </table></div>`;
}

export default {
  async render(ctx) {
    let catalog = await api.get("/api/saves", { signal: ctx.signal });

    const paint = () => {
      const worlds = catalog.worlds || [];
      if (!worlds.some(world => world.name === selectedWorld)) selectedWorld = (worlds.find(world => (world.saves || []).some(save => save.active)) || worlds[0])?.name || "";
      const world = worlds.find(item => item.name === selectedWorld);
      ctx.main.innerHTML = html`<div id="saves-page">
        ${pageHeader(t("nav_saves"), t("savesHelp"))}
        <div class="stack-lg">
          <div data-show-when="!stopped" hidden>${callout("warning", "alert", t("savesLocked"))}</div>
          <div class="saves-layout">
            <section class="card"><div class="card-header"><h3>${t("worlds")}</h3></div>${worldList(worlds)}</section>
            <section class="card">
              <div class="card-header"><h3>${world ? world.name : t("selectWorld")}</h3>${world ? html`<span class="subtle">${kindLabel(world)}</span>` : ""}</div>
              ${saveTable(world)}
            </section>
          </div>
          <section class="card">
            <div class="card-header"><h3>${t("importTitle")}</h3><p>${t("importHelp")}</p></div>
            <form class="card-body" id="import-form">
              <div class="row">
                <label class="field field-grow"><span class="visually-hidden">${t("importTitle")}</span><input type="file" id="package" accept=".zip" data-requires="stopped"></label>
                <button class="button button-primary" data-requires="stopped">${icon("upload")}${t("import")}</button>
              </div>
            </form>
          </section>
        </div>
      </div>`.value;
      ctx.gates();
      bind();
    };

    const reload = async () => {
      catalog = await api.get("/api/saves");
      if (ctx.isCurrent()) paint();
    };

    const bind = () => {
      const page = ctx.main.querySelector("#saves-page");
      for (const button of page.querySelectorAll(".world-button")) {
        button.addEventListener("click", () => { selectedWorld = button.dataset.world; paint(); });
      }
      const actions = {
        switch: (button, world, game) => withBusy(button, t("working"), async () => {
          const doc = await api.get("/api/config");
          await api.post("/api/saves/switch", { world, game, hash: doc.hash });
          toast(t("switchSave", { world, save: game }), "success");
          await reload();
        }),
        export: (button, world, game) => withBusy(button, t("working"), async () => {
          const backup = await api.post("/api/saves/export", { world, game });
          toast(t("exportedSave", { name: backup.name }), "success");
        }),
        delete: async (button, world, game) => {
          if (!(await confirmAction({ title: t("deleteSaveTitle", { world, save: game }), message: t("deleteSaveConfirm"), confirmLabel: t("delete"), danger: true }))) return;
          await withBusy(button, t("working"), async () => {
            const backup = await api.delete(`/api/saves/${encodeURIComponent(world)}/${encodeURIComponent(game)}`, { confirm: true });
            toast(t("deletedSave", { name: backup.name }), "success");
            await reload();
          });
        },
      };
      for (const button of page.querySelectorAll("[data-action]")) {
        button.addEventListener("click", () => actions[button.dataset.action](button, button.dataset.world, button.dataset.game));
      }
      page.querySelector("#import-form").addEventListener("submit", event => {
        event.preventDefault();
        const file = page.querySelector("#package").files[0];
        if (!file) {
          toast(t("choosePackage"), "error");
          return;
        }
        withBusy(event.submitter || event.target.querySelector("button"), t("working"), async () => {
          const form = new FormData();
          form.append("package", file);
          await api.upload("/api/saves/import", form);
          toast(t("importedSave", { name: file.name }), "success");
          await reload();
        });
      });
    };

    paint();
  },
};
