import { html } from "./html.js";
import { icon } from "./icons.js";
import { t } from "./i18n.js";
import { gates, state, stateTone } from "./store.js";

// ---------- Toasts ----------

const TOAST_ICONS = { success: "checkCircle", error: "xCircle", info: "info" };

export function toast(message, tone = "info") {
  const region = document.querySelector("#toasts");
  if (!region || !message) return;
  const node = document.createElement("div");
  node.className = `toast toast-${tone}`;
  node.innerHTML = html`${icon(TOAST_ICONS[tone] || "info")}<p></p><button class="icon-button" aria-label="${t("dismiss")}">${icon("x")}</button>`.value;
  node.querySelector("p").textContent = message;
  const close = () => node.remove();
  node.querySelector("button").onclick = close;
  region.append(node);
  setTimeout(close, tone === "error" ? 9000 : 4500);
}

export const toastError = error => toast(error?.message || String(error), "error");

// ---------- Confirm dialog ----------

export function confirmAction({ title, message = "", confirmLabel = t("confirm"), danger = false }) {
  const dialog = document.querySelector("#confirm-dialog");
  if (!dialog?.showModal) return Promise.resolve(window.confirm(`${title}\n\n${message}`));
  dialog.querySelector("#confirm-title").textContent = title;
  dialog.querySelector("#confirm-message").textContent = message;
  const accept = dialog.querySelector("#confirm-accept");
  accept.textContent = confirmLabel;
  accept.className = `button ${danger ? "button-danger" : "button-primary"}`;
  dialog.returnValue = "";
  dialog.showModal();
  dialog.querySelector('button[value="cancel"]').focus();
  return new Promise(resolve => dialog.addEventListener("close", () => resolve(dialog.returnValue === "confirm"), { once: true }));
}

// ---------- Busy buttons ----------

/** Run an async action while the button shows progress; errors become toasts. */
export async function withBusy(button, pendingText, action) {
  if (!button || button.getAttribute("aria-busy") === "true") return undefined;
  const label = button.innerHTML;
  button.setAttribute("aria-busy", "true");
  button.disabled = true;
  if (pendingText) button.textContent = pendingText;
  try {
    return await action();
  } catch (error) {
    if (error?.name !== "AbortError") toastError(error);
    return undefined;
  } finally {
    if (button.isConnected) {
      button.removeAttribute("aria-busy");
      button.innerHTML = label;
      button.disabled = false;
      applyGates(button.parentElement || document);
    }
  }
}

// ---------- State gates ----------

/**
 * Controls declare data-requires="stopped|running|force" and are enabled only
 * in that server state; data-locked="true" keeps them disabled regardless.
 * Elements with data-show-when show only while the gate holds. Updating
 * controls in place keeps unsaved form input when the server state changes.
 */
export function applyGates(root = document, status = state.status) {
  for (const node of root.querySelectorAll("[data-requires]")) {
    if (node.getAttribute("aria-busy") === "true") continue;
    const gate = gates[node.dataset.requires];
    const open = gate ? gate(status) : true;
    const disabled = !open || node.dataset.locked === "true";
    if ("disabled" in node && node.tagName !== "DIV") node.disabled = disabled;
    else node.setAttribute("aria-disabled", String(disabled));
  }
  for (const node of root.querySelectorAll("[data-show-when]")) {
    const [gateName, negate] = node.dataset.showWhen.startsWith("!") ? [node.dataset.showWhen.slice(1), true] : [node.dataset.showWhen, false];
    const gate = gates[gateName];
    node.hidden = gate ? gate(status) === negate : false;
  }
}

// ---------- Small components ----------

export function stateBadge(status) {
  const value = status?.state || "unknown";
  const tone = stateTone(value);
  return html`<span class="badge ${tone === "neutral" ? "" : `badge-${tone}`}"><span class="status-dot" aria-hidden="true"></span>${t(`state_${value}`)}</span>`;
}

export const callout = (tone, iconName, body) =>
  html`<div class="callout callout-${tone}">${icon(iconName)}<div>${body}</div></div>`;

export const emptyState = (iconName, title, detail = "") =>
  html`<div class="empty">${icon(iconName)}<strong>${title}</strong>${detail ? html`<p>${detail}</p>` : ""}</div>`;

export const pageHeader = (title, description, actions = "") =>
  html`<div class="page-header"><div><h2>${title}</h2>${description ? html`<p>${description}</p>` : ""}</div>${actions ? html`<div class="button-group">${actions}</div>` : ""}</div>`;

export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast(t("copied"), "success");
  } catch {
    toast(t("copyUnsupported"), "error");
  }
}
