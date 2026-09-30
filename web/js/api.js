import { t, tOr } from "./i18n.js";

// JSON API client. Every state-changing request carries the per-process
// session token that the backend checks alongside Host and Origin.

let token = "";

// Codes whose backend message adds nothing to the localized summary.
const SELF_EXPLANATORY = new Set([
  "forbidden", "not_found", "invalid_request", "mods_locked", "mod_conflict", "invalid_mod_name",
  "config_changed", "server_busy", "performance_unavailable", "firewall_stale", "dashboard_auth_required",
]);

/** Localized text for an API error: a summary per code, plus the backend detail when useful. */
export function errorMessage(code, message, status) {
  const summary = code ? tOr(`error_${code}`, "") : "";
  if (!summary) return message || `HTTP ${status}`;
  if (!message || SELF_EXPLANATORY.has(code)) return summary;
  return t("errorWithDetail", { summary, detail: message });
}

export class ApiError extends Error {
  constructor(message, status, data) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.data = data;
  }
}

export async function request(path, { method = "GET", body, form, signal } = {}) {
  const headers = new Headers();
  let payload;
  if (method !== "GET") headers.set("X-Panel-Token", token);
  if (form) {
    payload = form;
  } else if (method !== "GET") {
    headers.set("Content-Type", "application/json");
    payload = JSON.stringify(body ?? {});
  }
  const response = await fetch(path, { method, headers, body: payload, signal });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new ApiError(errorMessage(data.code, data.message, response.status), response.status, data);
  return data;
}

export const api = {
  get: (path, options) => request(path, options),
  post: (path, body, options) => request(path, { ...options, method: "POST", body }),
  put: (path, body, options) => request(path, { ...options, method: "PUT", body }),
  delete: (path, body, options) => request(path, { ...options, method: "DELETE", body }),
  upload: (path, form, options) => request(path, { ...options, method: "POST", form }),
};

export async function startSession() {
  const session = await api.get("/api/session");
  token = session.token;
  return session;
}
