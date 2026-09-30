// Tagged-template HTML builder. Interpolated values are escaped unless they
// come from html`` or raw(); arrays are joined; null, undefined and false
// render nothing.

const ENTITIES = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };

export class SafeHTML {
  constructor(value) { this.value = value; }
  toString() { return this.value; }
}

export const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, c => ENTITIES[c]);

export const raw = value => new SafeHTML(String(value));

function renderValue(value) {
  if (value instanceof SafeHTML) return value.value;
  if (Array.isArray(value)) return value.map(renderValue).join("");
  if (value === null || value === undefined || value === false) return "";
  return escapeHTML(value);
}

export function html(strings, ...values) {
  let out = strings[0];
  for (let i = 0; i < values.length; i++) out += renderValue(values[i]) + strings[i + 1];
  return new SafeHTML(out);
}

/** Boolean attribute helper: html`<button ${attr("disabled", locked)}>` */
export const attr = (name, enabled) => (enabled ? raw(` ${name}`) : "");
