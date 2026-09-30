import zhCN from "./locales/zh-CN.js";
import zhTW from "./locales/zh-TW.js";
import en from "./locales/en.js";

export const catalogs = { "zh-CN": zhCN, "zh-TW": zhTW, en };
export const languages = Object.keys(catalogs);

let current = "zh-CN";

export function setLanguage(language) {
  current = catalogs[language] ? language : "zh-CN";
  return current;
}

export const getLanguage = () => current;
export const isChinese = () => current.startsWith("zh");

/** Translate a key, substituting {name} placeholders from vars. */
export function t(key, vars = {}) {
  const template = catalogs[current][key] ?? catalogs.en[key] ?? key;
  return template.replace(/\{(\w+)\}/g, (match, name) => (name in vars ? String(vars[name]) : match));
}

/** Translate only when the catalog has the key; otherwise return the fallback. */
export function tOr(key, fallback, vars = {}) {
  return key in catalogs[current] || key in catalogs.en ? t(key, vars) : fallback;
}
