// Helpers for rendering 7 Days to Die log lines ("... INF/WRN/ERR/EXC ...").

export function logLevel(line) {
  if (/\b(ERR|EXC)\b/.test(line)) return "error";
  if (/\bWRN\b/.test(line)) return "warn";
  return "";
}

/** Append lines to a <pre>, keeping at most `limit` rows, and follow the tail if asked. */
export function appendLogLines(pre, lines, { limit = 500, follow = true } = {}) {
  if (!pre || !lines.length) return;
  const fragment = document.createDocumentFragment();
  for (const line of lines) {
    const row = document.createElement("span");
    const level = logLevel(line);
    if (level) row.className = `is-${level}`;
    row.textContent = `${line}\n`;
    fragment.append(row);
  }
  pre.querySelector(".log-empty")?.remove();
  pre.append(fragment);
  while (pre.childElementCount > limit) pre.firstElementChild.remove();
  if (follow) pre.scrollTop = pre.scrollHeight;
}

/** Batch log writes into one DOM update per animation frame. */
export function logWriter(pre, options) {
  let pending = [];
  let scheduled = false;
  return line => {
    pending.push(line);
    if (scheduled) return;
    scheduled = true;
    requestAnimationFrame(() => {
      scheduled = false;
      const lines = pending;
      pending = [];
      appendLogLines(pre, lines, typeof options === "function" ? options() : options);
    });
  };
}
