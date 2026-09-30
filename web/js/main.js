import { startSession } from "./api.js";
import { initConsole } from "./console.js";
import { connectEvents } from "./events.js";
import { formatUptime, knownBuild } from "./format.js";
import { html } from "./html.js";
import { getLanguage, languages, setLanguage, t } from "./i18n.js";
import { icon } from "./icons.js";
import { on, refreshStatus, state } from "./store.js";
import { applyGates, callout, confirmAction, stateBadge } from "./ui.js";
import overview from "./pages/overview.js";
import server from "./pages/server.js";
import config from "./pages/config.js";
import sandbox from "./pages/sandbox.js";
import mods from "./pages/mods.js";
import saves from "./pages/saves.js";
import backups from "./pages/backups.js";
import firewall from "./pages/firewall.js";
import players from "./pages/players.js";
import schedule from "./pages/schedule.js";
import panel from "./pages/panel.js";

export const PAGES = [
  { id: "overview", group: "navGroupRun", icon: "overview", page: overview },
  { id: "server", group: "navGroupRun", icon: "server", page: server },
  { id: "players", group: "navGroupRun", icon: "users", page: players },
  { id: "schedule", group: "navGroupRun", icon: "calendar", page: schedule },
  { id: "config", group: "navGroupConfig", icon: "config", page: config },
  { id: "sandbox", group: "navGroupConfig", icon: "sandbox", page: sandbox },
  { id: "mods", group: "navGroupConfig", icon: "mods", page: mods },
  { id: "saves", group: "navGroupData", icon: "saves", page: saves },
  { id: "backups", group: "navGroupData", icon: "backups", page: backups },
  { id: "firewall", group: "navGroupSystem", icon: "firewall", page: firewall },
  { id: "panel", group: "navGroupSystem", icon: "panel", page: panel },
];

const THEMES = ["system", "light", "dark"];
const storage = {
  get(key) { try { return localStorage.getItem(key); } catch { return null; } },
  set(key, value) { try { localStorage.setItem(key, value); } catch { /* private mode */ } },
};

const main = document.querySelector("#main");
let current = null;   // { entry, controller, cleanups, guard }
let navigationId = 0;

// ---------- Preferences & static text ----------

function applyTheme(theme) {
  if (theme === "system") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = theme;
}

function translateStatic() {
  document.documentElement.lang = getLanguage();
  for (const node of document.querySelectorAll("[data-i18n]")) node.textContent = t(node.dataset.i18n);
  for (const node of document.querySelectorAll("[data-i18n-aria-label]")) node.setAttribute("aria-label", t(node.dataset.i18nAriaLabel));
  document.querySelector("#language").value = getLanguage();
}

function renderNav() {
  const groups = [...new Set(PAGES.map(entry => entry.group))];
  document.querySelector("#nav").innerHTML = html`${groups.map(group => html`
    <div class="nav-group">
      <div class="nav-group-title">${t(group)}</div>
      ${PAGES.filter(entry => entry.group === group).map(entry => html`
        <a class="nav-link" href="#/${entry.id}" ${current?.entry.id === entry.id ? html`aria-current="page"` : ""}>${icon(entry.icon)}<span>${t(`nav_${entry.id}`)}</span></a>`)}
    </div>`)}`.value;
}

// ---------- Top bar ----------

function renderTopbar() {
  const status = state.status;
  const players = Number.isFinite(status.onlinePlayers) ? status.onlinePlayers : null;
  const parts = [stateBadge(status)];
  if (status.state === "running" || status.state === "starting") {
    if (players !== null) parts.push(html`<span class="topbar-meta">${icon("users")}${t("playersShort", { count: players })}</span>`);
    if (status.startedAt) parts.push(html`<span class="topbar-meta" data-uptime>${icon("clock")}${formatUptime(status.startedAt)}</span>`);
  }
  if (knownBuild(status.build)) parts.push(html`<span class="topbar-meta">${icon("tag")}${status.build}</span>`);
  document.querySelector("#topbar-status").innerHTML = html`${parts}`.value;
}

// ---------- Router ----------

function pageFromHash() {
  const id = location.hash.replace(/^#\/?/, "");
  return PAGES.find(entry => entry.id === id) || PAGES[0];
}

async function navigate(entry) {
  if (current?.guard && current.entry.id !== entry.id) {
    const dirty = current.guard();
    if (dirty && !(await confirmAction({ title: t("unsavedLeaveTitle"), message: t("unsavedLeave"), confirmLabel: t("leavePage"), danger: true }))) {
      history.replaceState(null, "", `#/${current.entry.id}`);
      return;
    }
  }
  const id = ++navigationId;
  if (current) {
    current.controller.abort();
    current.cleanups.forEach(cleanup => cleanup());
  }
  const controller = new AbortController();
  const cleanups = [];
  current = { entry, controller, cleanups, guard: null };
  document.title = `${t(`nav_${entry.id}`)} · ${t("appName")}`;
  document.querySelector("#page-title").textContent = t(`nav_${entry.id}`);
  renderNav();
  closeMenu();
  main.innerHTML = '<div class="skeleton" aria-busy="true"><div></div><div></div></div>';

  const ctx = {
    main,
    signal: controller.signal,
    isCurrent: () => id === navigationId,
    onCleanup: fn => cleanups.push(fn),
    on: (type, fn) => cleanups.push(on(type, fn)),
    setGuard: fn => { if (id === navigationId) current.guard = fn; },
    rerender: () => navigate(entry),
    gates: () => applyGates(main),
  };
  try {
    await entry.page.render(ctx);
    if (id !== navigationId) return;
    applyGates(main);
    ctx.on("status", () => applyGates(main));
  } catch (error) {
    if (error?.name === "AbortError" || id !== navigationId) return;
    main.innerHTML = html`${callout("danger", "alert", html`<strong>${t("loadFailed", { error: error.message })}</strong>`)}
      <p class="mt-4"><button class="button button-secondary" id="retry-page">${icon("refresh")}${t("retry")}</button></p>`.value;
    main.querySelector("#retry-page").onclick = () => navigate(entry);
  }
}

// ---------- Mobile menu ----------

function openMenu() {
  document.body.classList.add("menu-open");
  document.querySelector("#sidebar-scrim").hidden = false;
  document.querySelector("#menu-toggle").setAttribute("aria-expanded", "true");
}

function closeMenu() {
  document.body.classList.remove("menu-open");
  document.querySelector("#sidebar-scrim").hidden = true;
  document.querySelector("#menu-toggle").setAttribute("aria-expanded", "false");
}

// ---------- Boot ----------

async function boot() {
  setLanguage(languages.includes(storage.get("sevenpanel.language")) ? storage.get("sevenpanel.language") : "zh-CN");
  const theme = THEMES.includes(storage.get("sevenpanel.theme")) ? storage.get("sevenpanel.theme") : "system";
  applyTheme(theme);
  document.querySelector("#theme").value = theme;
  translateStatic();
  document.querySelector("#menu-toggle").innerHTML = icon("menu").value;

  document.querySelector("#language").addEventListener("change", event => {
    storage.set("sevenpanel.language", setLanguage(event.target.value));
    translateStatic();
    renderTopbar();
    if (current) navigate(current.entry);
  });
  document.querySelector("#theme").addEventListener("change", event => {
    storage.set("sevenpanel.theme", event.target.value);
    applyTheme(event.target.value);
  });
  document.querySelector("#menu-toggle").addEventListener("click", () => (document.body.classList.contains("menu-open") ? closeMenu() : openMenu()));
  document.querySelector("#sidebar-scrim").addEventListener("click", closeMenu);
  window.addEventListener("hashchange", () => navigate(pageFromHash()));
  window.addEventListener("beforeunload", event => {
    if (current?.guard?.()) event.preventDefault();
  });

  initConsole();
  on("status", renderTopbar);
  setInterval(() => {
    const node = document.querySelector("[data-uptime]");
    if (node && state.status.startedAt) node.lastChild.textContent = formatUptime(state.status.startedAt);
  }, 1000);

  // Player counts and the detected build change without a state event.
  setInterval(() => { if (!document.hidden) refreshStatus().catch(() => {}); }, 15000);

  const session = await startSession();
  document.querySelector("#app-version").textContent = session.version ? ` · ${session.version}` : "";
  await refreshStatus();
  connectEvents();
  await navigate(pageFromHash());
}

boot().catch(error => {
  main.innerHTML = callout("danger", "alert", html`<strong>${t("loadFailed", { error: error.message })}</strong>`).value;
});
