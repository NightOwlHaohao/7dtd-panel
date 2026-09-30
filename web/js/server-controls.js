import { api } from "./api.js";
import { html } from "./html.js";
import { icon } from "./icons.js";
import { t } from "./i18n.js";
import { setStatus, state } from "./store.js";
import { applyGates, confirmAction, toast, withBusy } from "./ui.js";

/** Start / stop / force-stop buttons, enabled by the current server state. */
export function serverControls({ vertical = false } = {}) {
  return html`<div class="${vertical ? "hero-actions" : "button-group"}">
    <button class="button button-primary" data-server-action="start" data-requires="stopped">${icon("play")}${t("start")}</button>
    <button class="button button-secondary" data-server-action="stop" data-requires="running">${icon("stop")}${t("stop")}</button>
    <button class="button button-danger-outline" data-server-action="force" data-requires="force">${icon("power")}${t("forceStop")}</button>
  </div>`;
}

export function bindServerControls(root) {
  const handlers = {
    start: button => withBusy(button, t("working"), async () => {
      setStatus(await api.post("/api/server/start"));
      toast(t("started"), "success");
    }),
    stop: async button => {
      if (!(await confirmAction({ title: t("stopTitle"), message: t("stopMessage"), confirmLabel: t("stop") }))) return;
      await withBusy(button, t("state_stopping"), async () => {
        setStatus(await api.post("/api/server/stop"));
        toast(t("stopped"), "success");
      });
    },
    force: async button => {
      if (!(await confirmAction({ title: t("forceStopTitle"), message: t("forceStopMessage"), confirmLabel: t("forceStop"), danger: true }))) return;
      await withBusy(button, t("working"), async () => {
        setStatus(await api.post("/api/server/force-stop", { confirm: state.status.forceStopToken }));
        toast(t("forceStopped"), "success");
      });
    },
  };
  for (const button of root.querySelectorAll("[data-server-action]")) {
    button.addEventListener("click", () => handlers[button.dataset.serverAction](button));
  }
  applyGates(root);
}
