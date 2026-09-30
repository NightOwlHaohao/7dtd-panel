import assert from "node:assert/strict";
import test from "node:test";
import { chartSeries, placeEndLabels, pruneHistory, WINDOW_MS } from "../js/chart.js";
import { formatBytes, formatUptime } from "../js/format.js";
import { setLanguage } from "../js/i18n.js";
import { logLevel } from "../js/logs.js";
import { collectUpdates, SECRET_MASK } from "../js/pages/config.js";
import { filterMods, problemText } from "../js/pages/mods.js";
import { canForceStop, gates, stateTone } from "../js/store.js";

setLanguage("en");

test("performance history keeps only the last 60 seconds", () => {
  const now = Date.parse("2026-01-01T00:01:00Z");
  const at = offset => ({ timestamp: new Date(now - offset).toISOString(), hostCpu: { available: true, value: 10 } });
  const kept = pruneHistory([at(WINDOW_MS + 1), at(30_000), at(0), { timestamp: "nope" }], now);
  assert.equal(kept.length, 2);
});

test("chart series skip unavailable metrics and clamp to the plot", () => {
  const now = Date.parse("2026-01-01T00:01:00Z");
  const series = chartSeries([{ timestamp: new Date(now).toISOString(), hostCpu: { available: true, value: 150 }, hostGpu: { available: false }, memory: { available: true, percent: 40 } }], now);
  assert.equal(series.find(item => item.key === "gpu").points.length, 0);
  const cpu = series.find(item => item.key === "cpu").points[0];
  assert.ok(cpu.y >= 10 && cpu.y <= 156);
});

test("end labels never overlap", () => {
  const placed = placeEndLabels([{ key: "a", y: 50 }, { key: "b", y: 52 }, { key: "c", y: 51 }]);
  for (let i = 1; i < placed.length; i++) assert.ok(placed[i].y - placed[i - 1].y >= 13);
});

test("formatting helpers", () => {
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatBytes(1536), "1.5 KB");
  assert.equal(formatBytes(3 * 1024 ** 3), "3 GB");
  const now = Date.parse("2026-01-01T02:03:04Z");
  assert.equal(formatUptime("2026-01-01T00:00:00Z", now), "2 h 3 min");
  assert.equal(formatUptime("2026-01-01T02:01:00Z", now), "2 min 4 s");
});

test("server state gates", () => {
  assert.equal(gates.stopped({ state: "stopped" }), true);
  assert.equal(gates.running({ state: "stopped" }), false);
  assert.equal(canForceStop({ state: "running" }), false);
  assert.equal(canForceStop({ state: "stop_timeout", forceStopToken: "x" }), true);
  assert.equal(canForceStop({ state: "stopped", forceStopToken: "x" }), false);
  assert.equal(stateTone("crashed"), "danger");
});

test("config updates include only changed values and never an untouched secret", () => {
  const controls = [
    { dataset: { key: "ServerName" }, value: "New" },
    { dataset: { key: "ServerPassword" }, value: SECRET_MASK },
    { dataset: { key: "ServerPort" }, value: "26900" },
  ];
  const initial = new Map([["ServerName", "Old"], ["ServerPassword", SECRET_MASK], ["ServerPort", "26900"]]);
  assert.deepEqual(collectUpdates(controls, initial), { ServerName: "New" });
});

test("mod filters and problem text", () => {
  const mods = [
    { name: "Alpha", author: "Ann", enabled: true, dependencies: [{ id: "Core" }], problems: [] },
    { name: "Beta", author: "Bob", enabled: false, dependencies: [], problems: [{ kind: "missing_dependency", dependency: "X" }] },
    { name: "Gamma", author: "Ann", unknown: true, dependencies: [], problems: [] },
  ];
  assert.deepEqual(filterMods(mods, { name: "", author: "ann", status: "all", problem: "all" }).map(mod => mod.name), ["Alpha", "Gamma"]);
  assert.deepEqual(filterMods(mods, { name: "", author: "", status: "unknown", problem: "all" }).map(mod => mod.name), ["Gamma"]);
  assert.deepEqual(filterMods(mods, { name: "", author: "", status: "all", problem: "problem" }).map(mod => mod.name), ["Beta"]);
  assert.deepEqual(filterMods(mods, { name: "", author: "", status: "all", problem: "dependency" }).map(mod => mod.name), ["Alpha"]);
  assert.equal(problemText({ kind: "missing_dependency", dependency: "X" }), "Missing dependency X");
  assert.equal(problemText({ kind: "brand_new" }), "Dependency problem");
});

test("7DTD log levels", () => {
  assert.equal(logLevel("2026-01-01T00:00:00 1.0 ERR Something broke"), "error");
  assert.equal(logLevel("2026-01-01T00:00:00 1.0 EXC NullReference"), "error");
  assert.equal(logLevel("2026-01-01T00:00:00 1.0 WRN Careful"), "warn");
  assert.equal(logLevel("2026-01-01T00:00:00 1.0 INF Fine"), "");
});

test("API errors are localized by code, keeping useful backend detail", async () => {
  const { errorMessage } = await import("../js/api.js");
  const { setLanguage: use } = await import("../js/i18n.js");
  use("zh-CN");
  assert.equal(errorMessage("server_start_failed", "started process executable does not match server executable", 400), "启动服务器失败：started process executable does not match server executable");
  assert.equal(errorMessage("mods_locked", "服务器未停止，Mod 更改已锁定。", 409), "服务器未停止，Mod 更改已锁定");
  assert.equal(errorMessage("brand_new_code", "raw text", 500), "raw text");
  assert.equal(errorMessage(undefined, undefined, 502), "HTTP 502");
  use("en");
});
