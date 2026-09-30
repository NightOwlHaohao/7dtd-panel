import assert from "node:assert/strict";
import test from "node:test";
import { describeOptions, encodeSandboxCode, optionValueIndex, parseSandboxCode, setOption, SandboxCodeError } from "../js/sandbox-code.js";

test("parse and encode round-trip", () => {
  const code = "AAABABC";
  const draft = parseSandboxCode(code);
  assert.deepEqual(draft, { header: "A", records: [{ OptionID: 0, ValueIndex: 1 }, { OptionID: 1, ValueIndex: 2 }] });
  assert.equal(encodeSandboxCode(draft), code);
  assert.deepEqual(parseSandboxCode(" AAAB\r\n"), { header: "A", records: [{ OptionID: 0, ValueIndex: 1 }] });
});

test("invalid codes report translatable positions", () => {
  const failure = (code, key, vars) => assert.throws(() => parseSandboxCode(code), error => error instanceof SandboxCodeError && error.key === key && JSON.stringify(error.vars) === JSON.stringify(vars));
  failure("", "sandboxInvalidLength", {});
  failure("AAB", "sandboxInvalidLength", {});
  failure("1AAB", "sandboxInvalidAt", { record: 0, character: 1 });
  failure("AAAbAAC", "sandboxInvalidAt", { record: 0, character: 2 });
  failure("AAABAAC", "sandboxDuplicate", { record: 1, id: 0 });
});

test("setOption updates or appends records", () => {
  const draft = parseSandboxCode("ABAA");
  setOption(draft, 26, 3);
  setOption(draft, 27, 1);
  assert.equal(encodeSandboxCode(draft), "ABADBBB");
});

test("optionValueIndex accepts numbers, numeric strings and letters", () => {
  assert.equal(optionValueIndex(3), 3);
  assert.equal(optionValueIndex("4"), 4);
  assert.equal(optionValueIndex("C"), 2);
  assert.equal(optionValueIndex({ index: 5 }), 5);
  assert.equal(optionValueIndex(26), null);
  assert.equal(optionValueIndex("x"), null);
});

test("describeOptions prefers localized names and marks non-numeric ids read-only", () => {
  const payload = { options: [
    { id: 3, key: "XPMultiplier", category: "General", valueSet: [{ localizedName: "50%" }, { localizedName: "100%" }] },
    { id: "abc", key: "Weird", valueSet: ["a"] },
  ] };
  const options = describeOptions(payload, [{ OptionID: 3, ValueIndex: 1 }], {}, {
    chinese: true, labels: { XPMultiplier: "经验倍率" }, fallbackCategory: "Other",
    untranslated: name => `?${name}`, optionLabel: id => `#${id}`,
  });
  assert.equal(options[0].title, "经验倍率");
  assert.equal(options[0].initial, 1);
  assert.deepEqual(options[0].choices, ["50%", "100%"]);
  assert.equal(options[0].editable, true);
  assert.equal(options[1].editable, false);
  assert.equal(options[1].title, "?Weird");
});
