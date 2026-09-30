import { t, getLanguage } from "./i18n.js";

export const round1 = value => Math.round(Number(value) * 10) / 10;

export function formatBytes(value) {
  const bytes = Number(value);
  if (!Number.isFinite(bytes) || bytes < 0) return t("unknown");
  if (bytes >= 1024 ** 3) return t("gib", { count: round1(bytes / 1024 ** 3) });
  if (bytes >= 1024 ** 2) return t("mib", { count: round1(bytes / 1024 ** 2) });
  if (bytes >= 1024) return t("kib", { count: round1(bytes / 1024) });
  return t("bytes", { count: bytes });
}

export function formatPercent(value) {
  return Number.isFinite(Number(value)) ? `${round1(value)}%` : t("unavailable");
}

export function formatDateTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return t("timeUnknown");
  return date.toLocaleString(getLanguage(), { dateStyle: "medium", timeStyle: "short" });
}

export function formatAge(value, now = Date.now()) {
  const minutes = Math.floor((now - new Date(value).getTime()) / 60000);
  if (!Number.isFinite(minutes)) return t("timeUnknown");
  return minutes < 1 ? t("justNow") : t("minutesAgo", { minutes });
}

export function formatUptime(startedAt, now = Date.now()) {
  const seconds = Math.floor((now - new Date(startedAt).getTime()) / 1000);
  if (!Number.isFinite(seconds) || seconds < 0) return t("unavailable");
  const hours = Math.floor(seconds / 3600), minutes = Math.floor((seconds % 3600) / 60);
  return hours > 0 ? t("uptimeValue", { hours, minutes }) : t("uptimeShort", { minutes, seconds: seconds % 60 });
}

export const knownBuild = value => typeof value === "string" && value !== "" && value !== "unknown";
