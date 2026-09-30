// SandboxCode: one header letter followed by 3-letter records
// [OptionID high, OptionID low, ValueIndex], each letter A-Z (base 26).

export class SandboxCodeError extends Error {
  constructor(key, vars = {}) {
    super(key);
    this.name = "SandboxCodeError";
    this.key = key;
    this.vars = vars;
  }
}

const letter = value => String.fromCharCode(65 + value);

export function parseSandboxCode(raw) {
  const code = String(raw).replace(/[\r\n]/g, "").trim();
  if (!code.length || (code.length - 1) % 3) throw new SandboxCodeError("sandboxInvalidLength");
  if (!/^[A-Z]$/.test(code[0])) throw new SandboxCodeError("sandboxInvalidAt", { record: 0, character: 1 });
  const records = [];
  const seen = new Set();
  for (let offset = 1, record = 0; offset < code.length; offset += 3, record++) {
    const chunk = code.slice(offset, offset + 3);
    if (!/^[A-Z]{3}$/.test(chunk)) throw new SandboxCodeError("sandboxInvalidAt", { record, character: offset + 1 });
    const id = (chunk.charCodeAt(0) - 65) * 26 + chunk.charCodeAt(1) - 65;
    if (seen.has(id)) throw new SandboxCodeError("sandboxDuplicate", { record, id });
    seen.add(id);
    records.push({ OptionID: id, ValueIndex: chunk.charCodeAt(2) - 65 });
  }
  return { header: code[0], records };
}

export function encodeSandboxCode(draft) {
  return draft.header + draft.records.map(record => letter(Math.floor(record.OptionID / 26)) + letter(record.OptionID % 26) + letter(record.ValueIndex)).join("");
}

/** Set an option in place, appending a record when the code lacks it. */
export function setOption(draft, id, value) {
  const record = draft.records.find(row => row.OptionID === id);
  if (record) record.ValueIndex = value;
  else draft.records.push({ OptionID: id, ValueIndex: value });
  return draft;
}

/** A value index from a number, a numeric string or a single letter. */
export function optionValueIndex(value) {
  const text = String(value?.index ?? value ?? "").trim();
  const index = /^[A-Z]$/.test(text) ? text.charCodeAt(0) - 65 : Number(text);
  return Number.isInteger(index) && index >= 0 && index < 26 ? index : null;
}

export const sandboxOptionKey = option => String(option?.key ?? option?.details?.internalName ?? "");

/**
 * Normalise the Dashboard option payload into view models. `labels` maps option
 * keys to Chinese names for when the payload has no localized name.
 */
export function describeOptions(payload, records, texts, { chinese, labels, fallbackCategory, untranslated, optionLabel }) {
  return (payload?.options || []).map(option => {
    const rawID = option?.id ?? option?.optionId ?? option?.key;
    const id = Number(rawID);
    const numeric = typeof rawID === "number" || (typeof rawID === "string" && /^-?\d+$/.test(rawID.trim()));
    const editableID = numeric && Number.isInteger(id) && id >= 0 && id < 26 * 26;
    const category = String(option?.category ?? option?.group ?? option?.section ?? option?.details?.categoryName ?? fallbackCategory);
    const record = editableID && records.find(row => Number(row.OptionID) === id);
    const initial = optionValueIndex(record ? record.ValueIndex : option?.activeValue ?? option?.value ?? option?.defaultValue) ?? 0;
    const choices = [option?.valueSet, option?.values, option?.options, option?.options?.choices].find(value => Array.isArray(value) && value.length) || null;
    const rawName = String(option?.property ?? option?.propertyName ?? option?.name ?? option?.displayName ?? option?.details?.internalName ?? option?.key ?? optionLabel(id));
    const key = sandboxOptionKey(option);
    const english = texts?.[String(id)]?.labels?.en || rawName;
    const title = chinese ? option?.details?.localizedName || labels[key] || untranslated(english) : english;
    const meta = `${chinese && key ? `${english} / ${key}` : rawName}${editableID ? ` · #${id}` : ""}`;
    return {
      id,
      editable: Boolean(editableID && choices),
      category,
      title,
      meta,
      initial,
      choices: (choices || []).slice(0, 26).map(choice => String(choice?.localizedName ?? choice?.name ?? choice?.label ?? choice)),
      activeLabel: option?.activeValue?.localizedName ?? null,
      search: `${title} ${english} ${rawName} ${category}`.toLowerCase(),
    };
  });
}
