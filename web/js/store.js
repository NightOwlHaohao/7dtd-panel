import { api } from "./api.js";

// Shared runtime state fed by /api/status and the server-sent event stream.

export const LOG_LIMIT = 500;
export const ACTIVITY_LIMIT = 50;
const FORCE_STOP_STATES = ["starting", "running", "stopping", "stop_timeout", "shutdown_failed", "crashed"];

export const state = { status: { state: "unknown" }, logLines: [], activity: [] };

const listeners = { status: new Set(), log: new Set(), activity: new Set() };

export function on(type, listener) {
  listeners[type].add(listener);
  return () => listeners[type].delete(listener);
}

function emit(type, value) {
  for (const listener of listeners[type]) listener(value);
}

export function setStatus(status) {
  state.status = status && typeof status === "object" ? status : { state: "unknown" };
  emit("status", state.status);
}

export async function refreshStatus(signal) {
  const status = await api.get("/api/status", { signal });
  setStatus(status);
  return status;
}

export function pushLog(line) {
  state.logLines.push(line);
  if (state.logLines.length > LOG_LIMIT) state.logLines.splice(0, state.logLines.length - LOG_LIMIT);
  emit("log", line);
}

export function pushActivity(entry) {
  state.activity.unshift(entry);
  if (state.activity.length > ACTIVITY_LIMIT) state.activity.length = ACTIVITY_LIMIT;
  emit("activity", entry);
}

export const canForceStop = status => FORCE_STOP_STATES.includes(status?.state) && Boolean(status?.forceStopToken);

/** Predicates referenced by data-requires="..." on controls. */
export const gates = {
  stopped: status => status?.state === "stopped",
  running: status => status?.state === "running",
  force: canForceStop,
};

export function stateTone(value) {
  switch (value) {
    case "running": return "success";
    case "starting": case "stopping": return "info";
    case "stop_timeout": case "shutdown_failed": return "warning";
    case "crashed": return "danger";
    default: return "neutral";
  }
}
