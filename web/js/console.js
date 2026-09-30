import { api } from "./api.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { logLevel } from "./logs.js";
import { on } from "./store.js";
import { copyText, toastError } from "./ui.js";

// Bottom drawer showing the backend console store (/api/console), with
// channel/level filters and a Telnet command box.

const MAX_ENTRIES = 500;
const MIN_HEIGHT = 240;
const POLL_MS = 1500;

let entries = [];
let cursor = 0;
let open = false;
let paused = false;
let timer;
let unseenErrors = 0;
let lastTrigger;

export const mergeEntries = (current, incoming) =>
  [...new Map([...current, ...incoming].map(entry => [entry.cursor, entry])).values()]
    .sort((a, b) => a.cursor - b.cursor)
    .slice(-MAX_ENTRIES);

const $ = selector => document.querySelector(selector);

function renderEntries() {
  const log = $("#console-log");
  const level = $("#console-level").value;
  const visible = entries.filter(entry => level === "all" || entry.level === level);
  log.replaceChildren();
  if (!visible.length) {
    log.textContent = t("consoleEmpty");
    return;
  }
  const fragment = document.createDocumentFragment();
  for (const entry of visible) {
    const row = document.createElement("span");
    const tone = entry.level === "error" ? "error" : entry.level === "warn" ? "warn" : entry.level === "command" ? "command" : logLevel(entry.text);
    if (tone) row.className = `is-${tone}`;
    row.textContent = `${entry.level === "command" ? "> " : ""}${entry.text}\n`;
    fragment.append(row);
  }
  log.append(fragment);
  log.scrollTop = log.scrollHeight;
}

async function poll() {
  if (!open || paused) return;
  const channel = $("#console-channel").value;
  const data = await api.get(`/api/console?channel=${encodeURIComponent(channel)}&after=${cursor}&limit=500`);
  if (channel !== $("#console-channel").value) return;
  const incoming = data.entries || [];
  cursor = data.cursor || cursor;
  if (incoming.length || !entries.length) {
    entries = mergeEntries(entries, incoming);
    renderEntries();
  }
}

function schedule() {
  clearTimeout(timer);
  if (!open) return;
  timer = setTimeout(async () => {
    try { await poll(); } catch (error) { toastError(error); }
    schedule();
  }, POLL_MS);
}

function updateBadge() {
  const badge = $("#console-badge");
  const toggle = $("#console-toggle");
  badge.hidden = unseenErrors === 0;
  badge.textContent = unseenErrors > 99 ? "99+" : String(unseenErrors);
  toggle.setAttribute("aria-label", unseenErrors ? t("consoleErrors", { count: unseenErrors }) : t(open ? "consoleClose" : "consoleOpen"));
}

function setHeight(height) {
  const max = Math.max(MIN_HEIGHT, innerHeight - 96);
  const clamped = Math.max(MIN_HEIGHT, Math.min(max, height));
  document.documentElement.style.setProperty("--console-height", `${clamped}px`);
  const handle = $("#console-resize");
  handle.setAttribute("aria-valuemin", String(MIN_HEIGHT));
  handle.setAttribute("aria-valuemax", String(max));
  handle.setAttribute("aria-valuenow", String(clamped));
}

export async function openConsole(trigger = $("#console-toggle")) {
  open = true;
  lastTrigger = trigger;
  unseenErrors = 0;
  $("#console-drawer").hidden = false;
  document.body.classList.add("console-open");
  $("#console-toggle").setAttribute("aria-expanded", "true");
  updateBadge();
  try { await poll(); } catch (error) { toastError(error); }
  schedule();
}

export function closeConsole() {
  open = false;
  clearTimeout(timer);
  $("#console-drawer").hidden = true;
  document.body.classList.remove("console-open");
  $("#console-toggle").setAttribute("aria-expanded", "false");
  updateBadge();
  lastTrigger?.focus();
}

export function initConsole() {
  $("#console-close").innerHTML = icon("x").value;
  $("#console-toggle .console-toggle-icon").innerHTML = icon("terminal").value;
  setHeight(Math.round(innerHeight * 0.4));

  $("#console-toggle").addEventListener("click", event => (open ? closeConsole() : openConsole(event.currentTarget)));
  $("#console-close").addEventListener("click", closeConsole);
  $("#console-channel").addEventListener("change", () => {
    entries = [];
    cursor = 0;
    renderEntries();
    poll().catch(toastError);
  });
  $("#console-level").addEventListener("change", renderEntries);
  $("#console-pause").addEventListener("click", event => {
    paused = !paused;
    event.currentTarget.textContent = t(paused ? "consoleResume" : "consolePause");
    event.currentTarget.setAttribute("aria-pressed", String(paused));
    if (!paused) poll().catch(toastError);
  });
  $("#console-copy").addEventListener("click", () => copyText($("#console-log").textContent));
  $("#console-clear").addEventListener("click", () => {
    entries = [];
    renderEntries();
  });

  const handle = $("#console-resize");
  let dragging = false;
  handle.addEventListener("pointerdown", event => {
    dragging = true;
    handle.setPointerCapture(event.pointerId);
  });
  handle.addEventListener("pointermove", event => dragging && setHeight(innerHeight - event.clientY));
  handle.addEventListener("pointerup", () => { dragging = false; });
  handle.addEventListener("pointercancel", () => { dragging = false; });
  handle.addEventListener("keydown", event => {
    const step = event.key === "ArrowUp" ? 32 : event.key === "ArrowDown" ? -32 : 0;
    if (!step) return;
    event.preventDefault();
    setHeight(Number(handle.getAttribute("aria-valuenow")) + step);
  });

  const form = $("#console-form");
  const feedback = $("#console-feedback");
  form.addEventListener("submit", async event => {
    event.preventDefault();
    const input = $("#console-command");
    const send = form.querySelector("button");
    const command = input.value.trim();
    if (!command || send.disabled) return;
    send.disabled = true;
    send.setAttribute("aria-busy", "true");
    feedback.className = "field-feedback";
    feedback.textContent = t("sending");
    try {
      await api.post("/api/console/telnet", { command });
      input.value = "";
      feedback.className = "field-feedback is-success";
      feedback.textContent = t("commandSent");
      await poll();
    } catch (error) {
      feedback.className = "field-feedback is-error";
      feedback.textContent = t("commandFailed", { error: error.message });
    } finally {
      send.disabled = false;
      send.removeAttribute("aria-busy");
    }
  });

  document.addEventListener("keydown", event => {
    if (event.key === "Escape" && open && !document.querySelector("dialog[open]")) closeConsole();
  });

  on("log", line => {
    if (!open && logLevel(line) === "error") {
      unseenErrors++;
      updateBadge();
    }
  });
  updateBadge();
}
