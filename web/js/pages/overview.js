import { api } from "../api.js";
import { bindChartHover, chartLegend, pruneHistory, renderChart, setChartWidth } from "../chart.js";
import { openConsole } from "../console.js";
import { formatBytes, formatDateTime, formatPercent, formatUptime, knownBuild } from "../format.js";
import { html } from "../html.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { bindServerControls, serverControls } from "../server-controls.js";
import { state } from "../store.js";
import { callout, emptyState, stateBadge, toast, withBusy } from "../ui.js";

const POLL_MS = 2000;
let history = [];      // survives navigation so the chart keeps its window
let stale = false;

const metricOrUnavailable = metric => (metric?.available ? formatPercent(metric.value) : t("unavailable"));
const metricValue = metric => (metric?.available ? formatPercent(metric.value) : html`<span class="stat-na">—</span>`);
const width = metric => (metric?.available ? Math.max(0, Math.min(100, Number(metric.value))) : 0);

function statTile(label, value, detail, meterClass, percent) {
  return html`<div class="card stat">
    <span class="stat-label">${label}</span>
    <span class="stat-value">${value}</span>
    ${detail ? html`<span class="stat-detail">${detail}</span>` : ""}
    ${meterClass ? html`<div class="meter ${meterClass}" aria-hidden="true"><span style="width:${percent}%"></span></div>` : ""}
  </div>`;
}

function performanceTiles(sample) {
  const memory = sample?.memory;
  const game = html`${t("perfGameDetail", { cpu: metricOrUnavailable(sample?.gameCpu), memory: sample?.gameMemory?.available ? formatBytes(sample.gameMemory.value) : t("unavailable") })}`;
  return html`<div class="stat-grid">
    ${statTile(t("perfHostCpu"), metricValue(sample?.hostCpu), sample?.hostCpu?.available ? "" : t("unavailable"), "is-cpu", width(sample?.hostCpu))}
    ${statTile(t("perfGpu"), metricValue(sample?.hostGpu), sample?.hostGpu?.available ? "" : t("unavailable"), "is-gpu", width(sample?.hostGpu))}
    ${statTile(t("perfMemory"), memory?.available ? formatPercent(memory.percent) : html`<span class="stat-na">—</span>`,
      memory?.available ? t("perfMemoryDetail", { used: formatBytes(memory.usedBytes), total: formatBytes(memory.totalBytes) }) : t("unavailable"),
      "is-memory", memory?.available ? Math.min(100, Number(memory.percent)) : 0)}
    ${statTile(t("perfGameProcess"), metricValue(sample?.gameCpu), game, "", 0)}
  </div>`;
}

function performanceBody() {
  const sample = history.at(-1);
  const notice = stale
    ? callout("warning", "alert", history.length ? t("perfStale") : t("perfUnavailable"))
    : !history.length ? html`<p class="subtle">${t("perfWaiting")}</p>` : "";
  return html`${performanceTiles(sample)}
    <div class="card"><div class="card-header"><h3>${t("performance")}</h3><span class="subtle">${t("perfWindow")}</span></div>
      <div class="chart" id="perf-chart">${renderChart(history)}${chartLegend()}</div>
    </div>${notice}`;
}

function hero(status) {
  const players = Number.isFinite(status.onlinePlayers) ? status.onlinePlayers : null;
  const hint = { running: t("overviewRunningHint"), stopped: t("overviewStoppedHint"), starting: t("overviewStartingHint") }[status.state] || t("overviewBusyHint");
  return html`<section class="card hero" aria-labelledby="hero-title">
    <div>
      <div class="hero-state"><h2 id="hero-title" class="visually-hidden">${t("serverControlsTitle")}</h2>${stateBadge(status)}<span class="subtle">${hint}</span></div>
      <dl class="kv">
        <div><dt>${t("worldLabel")}</dt><dd>${status.world && status.world !== "unknown" ? status.world : t("unknown")}</dd></div>
        <div><dt>${t("saveLabel")}</dt><dd>${status.gameName && status.gameName !== "unknown" ? status.gameName : t("unknown")}</dd></div>
        <div><dt>${t("playersLabel")}</dt><dd>${players ?? "—"}</dd></div>
        <div><dt>${t("uptimeLabel")}</dt><dd data-overview-uptime>${["running", "starting"].includes(status.state) && status.startedAt ? formatUptime(status.startedAt) : "—"}</dd></div>
        <div><dt>${t("buildLabel")}</dt><dd>${knownBuild(status.build) ? status.build : t("unknown")}</dd></div>
      </dl>
      ${status.diagnostic ? html`<div class="mt-4">${callout("warning", "alert", html`<strong>${t("diagnostic")}</strong>${status.diagnostic}`)}</div>` : ""}
      ${status.lastCrash ? html`<div class="mt-4">${callout("danger", "alert", t("lastCrashNotice", { time: formatDateTime(status.lastCrash) }))}</div>` : ""}
    </div>
    ${serverControls({ vertical: true })}
  </section>`;
}

const SETUP_ITEMS = ["serverExe", "serverConfig", "userData", "steamCmd"];

function setupCard(setup) {
  const other = (setup.problems || []).filter(problem => !problem.startsWith("missing "));
  const allGood = SETUP_ITEMS.every(key => setup[key]) && !other.length;
  return html`<section class="card">
    <div class="card-header"><h3>${t("setupTitle")}</h3>${allGood ? html`<span class="badge badge-success">${icon("check")}${t("setupAllGood")}</span>` : ""}</div>
    <div class="card-body">
      <ul class="checklist">${SETUP_ITEMS.map(key => html`<li>${setup[key] ? html`<span class="ok">${icon("checkCircle", t("yes"))}</span>` : html`<span class="bad">${icon("xCircle", t("no"))}</span>`}<span>${t(`setup_${key}`)}</span></li>`)}</ul>
      ${other.length ? callout("warning", "alert", html`<strong>${t("setupOtherProblems")}</strong>${other.join("；")}`) : ""}
      <div class="button-group">
        <button class="button button-secondary" id="prepare">${icon("folder")}${t("prepareData")}</button>
        <button class="button button-secondary" id="point-userdata" ${setup.serverConfig ? "" : html`disabled`}>${icon("check")}${t("pointUserData")}</button>
      </div>
    </div>
  </section>`;
}

function activityCard() {
  const items = state.activity.slice(0, 8);
  return html`<section class="card">
    <div class="card-header"><h3>${t("activityTitle")}</h3><button class="button button-ghost button-sm" id="open-console">${icon("terminal")}${t("openConsole")}</button></div>
    ${items.length
      ? html`<ul class="activity">${items.map(item => html`<li><span class="badge">${item.source}</span><span>${item.message}</span><time datetime="${item.time.toISOString()}">${formatDateTime(item.time)}</time></li>`)}</ul>`
      : emptyState("clock", t("activityEmpty"))}
  </section>`;
}

export default {
  async render(ctx) {
    const setup = await api.get("/api/setup", { signal: ctx.signal });
    const status = state.status;
    ctx.main.innerHTML = html`<div class="stack-lg">
      <div id="hero">${hero(status)}</div>
      <section class="stack" id="performance" aria-label="${t("performance")}">${performanceBody()}</section>
      <div class="grid grid-2">
        <div id="setup">${setupCard(setup)}</div>
        <div id="activity">${activityCard()}</div>
      </div>
    </div>`.value;

    const bindHero = () => bindServerControls(ctx.main.querySelector("#hero"));
    bindHero();
    // Rebuild the hero on every status change; if a control is mid-action,
    // wait until it finishes so its busy state is not wiped.
    let heroTimer;
    const refreshHero = () => {
      clearTimeout(heroTimer);
      const heroNode = ctx.main.querySelector("#hero");
      if (!heroNode) return;
      if (heroNode.querySelector("[aria-busy='true']")) {
        heroTimer = setTimeout(refreshHero, 150);
        return;
      }
      heroNode.innerHTML = hero(state.status).value;
      bindHero();
    };
    ctx.on("status", refreshHero);
    ctx.onCleanup(() => clearTimeout(heroTimer));
    const uptimeTimer = setInterval(() => {
      const node = ctx.main.querySelector("[data-overview-uptime]");
      if (node && ["running", "starting"].includes(state.status.state) && state.status.startedAt) node.textContent = formatUptime(state.status.startedAt);
    }, 1000);
    ctx.onCleanup(() => clearInterval(uptimeTimer));

    const bindSetup = () => {
      const root = ctx.main.querySelector("#setup");
      root.querySelector("#prepare").onclick = event => withBusy(event.currentTarget, t("working"), async () => {
        await api.post("/api/setup/prepare");
        toast(t("directoryReady"), "success");
        root.innerHTML = setupCard(await api.get("/api/setup")).value;
        bindSetup();
      });
      root.querySelector("#point-userdata").onclick = event => withBusy(event.currentTarget, t("working"), async () => {
        const doc = await api.get("/api/config");
        await api.post("/api/setup/userdata", { hash: doc.hash });
        toast(t("userDataUpdated"), "success");
        root.innerHTML = setupCard(await api.get("/api/setup")).value;
        bindSetup();
      });
    };
    bindSetup();

    const bindActivity = () => {
      ctx.main.querySelector("#open-console").onclick = event => openConsole(event.currentTarget);
    };
    bindActivity();
    ctx.on("activity", () => {
      ctx.main.querySelector("#activity").innerHTML = activityCard().value;
      bindActivity();
    });

    const chartHost = () => ctx.main.querySelector("#perf-chart");
    const measure = () => {
      const host = chartHost();
      if (host) setChartWidth(host.clientWidth - 48);
    };
    const paint = () => {
      const node = ctx.main.querySelector("#performance");
      if (!node) return;
      node.innerHTML = performanceBody().value;
      bindChartHover(chartHost(), () => history);
    };
    measure();
    paint();
    const resize = new ResizeObserver(() => {
      const before = chartHost()?.querySelector("svg")?.getAttribute("width");
      measure();
      if (String(before) !== String(Math.max(320, Math.round(chartHost().clientWidth - 48)))) paint();
    });
    resize.observe(ctx.main.querySelector("#performance"));
    ctx.onCleanup(() => resize.disconnect());

    let timer;
    const poll = async () => {
      try {
        const sample = await api.get("/api/performance", { signal: ctx.signal });
        history = pruneHistory([...history, sample]);
        stale = false;
      } catch (error) {
        if (error?.name === "AbortError") return;
        history = pruneHistory(history);
        stale = true;
      }
      if (!ctx.isCurrent()) return;
      paint();
      timer = setTimeout(poll, POLL_MS);
    };
    ctx.onCleanup(() => clearTimeout(timer));
    poll();
  },
};
