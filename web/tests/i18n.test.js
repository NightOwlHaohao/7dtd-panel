import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";
import { catalogs, setLanguage, t } from "../js/i18n.js";

const root = new URL("..", import.meta.url).pathname;
const placeholders = text => [...text.matchAll(/\{(\w+)\}/g)].map(match => match[1]).sort().join(",");

function sources(dir) {
  return readdirSync(dir).flatMap(name => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return name === "locales" ? [] : sources(path);
    return name.endsWith(".js") ? [path] : [];
  });
}

test("every locale has the same keys and placeholders as zh-CN", () => {
  const reference = catalogs["zh-CN"];
  for (const [language, catalog] of Object.entries(catalogs)) {
    assert.deepEqual(Object.keys(catalog).sort(), Object.keys(reference).sort(), `${language} keys differ`);
    for (const [key, text] of Object.entries(catalog)) {
      assert.ok(text.trim(), `${language}.${key} is empty`);
      assert.equal(placeholders(text), placeholders(reference[key]), `${language}.${key} placeholders differ`);
    }
  }
});

test("every literal t() key used by the UI exists", () => {
  const keys = new Set(Object.keys(catalogs["zh-CN"]));
  const missing = [];
  for (const file of sources(join(root, "js"))) {
    for (const match of readFileSync(file, "utf8").matchAll(/\bt\("([\w-]+)"/g)) {
      if (!keys.has(match[1])) missing.push(`${file}: ${match[1]}`);
    }
  }
  const page = readFileSync(join(root, "index.html"), "utf8");
  for (const match of page.matchAll(/data-i18n(?:-aria-label)?="(\w+)"/g)) {
    if (!keys.has(match[1])) missing.push(`index.html: ${match[1]}`);
  }
  assert.deepEqual(missing, []);
});

test("t substitutes variables and falls back to English, then the key", () => {
  setLanguage("zh-CN");
  assert.equal(t("playersShort", { count: 3 }), "玩家 3");
  setLanguage("en");
  assert.equal(t("playersShort", { count: 3 }), "3 players");
  assert.equal(t("no_such_key"), "no_such_key");
  assert.equal(t("playersShort"), "{count} players");
  setLanguage("xx");
  assert.equal(t("state_running"), "运行中");
});
