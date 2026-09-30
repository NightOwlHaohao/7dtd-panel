import assert from "node:assert/strict";
import test from "node:test";
import { attr, escapeHTML, html, raw } from "../js/html.js";

test("interpolated values are escaped", () => {
  const name = `<img src=x onerror="alert(1)">&'`;
  assert.equal(html`<b>${name}</b>`.value, "<b>&lt;img src=x onerror=&quot;alert(1)&quot;&gt;&amp;&#39;</b>");
  assert.equal(html`<i title="${'"quoted"'}"></i>`.value, '<i title="&quot;quoted&quot;"></i>');
});

test("nested templates, arrays and raw values are not double-escaped", () => {
  const items = ["a<b", "c"].map(item => html`<li>${item}</li>`);
  assert.equal(html`<ul>${items}</ul>`.value, "<ul><li>a&lt;b</li><li>c</li></ul>");
  assert.equal(html`${raw("<br>")}`.value, "<br>");
});

test("null, undefined and false render nothing; zero renders", () => {
  assert.equal(html`${null}${undefined}${false}${0}`.value, "0");
  assert.equal(escapeHTML(undefined), "");
  assert.equal(html`<button${attr("disabled", true)}${attr("hidden", false)}>`.value, "<button disabled>");
});
