import { formatPercent } from "./format.js";
import { html } from "./html.js";
import { t } from "./i18n.js";

// 60-second line chart of host CPU, GPU and memory utilisation (0-100%).
// Series colours come from --series-1..3 (validated categorical slots);
// every line is also labelled at its end and the tiles above act as a table.

export const WINDOW_MS = 60_000;
export const SERIES = [
  { key: "cpu", label: "perfHostCpu", value: sample => (sample?.hostCpu?.available ? Number(sample.hostCpu.value) : NaN) },
  { key: "gpu", label: "perfGpu", value: sample => (sample?.hostGpu?.available ? Number(sample.hostGpu.value) : NaN) },
  { key: "memory", label: "perfMemory", value: sample => (sample?.memory?.available ? Number(sample.memory.percent) : NaN) },
];

// Geometry is in CSS pixels: the chart is re-rendered at its container width
// so axis text keeps its real size instead of scaling with the viewBox.
const G = { width: 600, height: 200, left: 40, right: 530, top: 10, bottom: 172 };
export function setChartWidth(width) {
  G.width = Math.max(320, Math.round(width));
  G.right = G.width - 70;
}
const clamp = value => Math.max(0, Math.min(100, value));
const x = (time, now) => G.left + ((time - (now - WINDOW_MS)) / WINDOW_MS) * (G.right - G.left);
const y = value => G.bottom - (clamp(value) / 100) * (G.bottom - G.top);

export function pruneHistory(history, now = Date.now()) {
  return history
    .filter(sample => {
      const time = new Date(sample?.timestamp).getTime();
      return Number.isFinite(time) && time >= now - WINDOW_MS && time <= now + 1000;
    })
    .slice(-40);
}

export function chartSeries(history, now = Date.now()) {
  return SERIES.map(series => ({
    ...series,
    points: history
      .map(sample => ({ time: new Date(sample?.timestamp).getTime(), value: series.value(sample) }))
      .filter(point => Number.isFinite(point.time) && Number.isFinite(point.value))
      .map(point => ({ ...point, x: x(point.time, now), y: y(point.value) })),
  }));
}

/** Spread end labels vertically so they never overlap (min 13 units apart). */
export function placeEndLabels(labels, gap = 13) {
  const sorted = [...labels].sort((a, b) => a.y - b.y);
  for (let i = 1; i < sorted.length; i++) sorted[i].y = Math.max(sorted[i].y, sorted[i - 1].y + gap);
  const overflow = sorted.length ? sorted.at(-1).y - (G.bottom + 4) : 0;
  if (overflow > 0) sorted.forEach(label => { label.y -= overflow; });
  return sorted;
}

export function renderChart(history, now = Date.now()) {
  const series = chartSeries(history, now);
  const grid = [0, 50, 100].map(value => html`<line x1="${G.left}" x2="${G.right}" y1="${y(value)}" y2="${y(value)}"/>`);
  const axis = html`
    <text x="${G.left - 6}" y="${y(100) + 4}" text-anchor="end">100%</text>
    <text x="${G.left - 6}" y="${y(50) + 4}" text-anchor="end">50%</text>
    <text x="${G.left - 6}" y="${y(0) + 4}" text-anchor="end">0%</text>
    <text x="${G.left}" y="${G.height - 6}">${t("perfAgo60")}</text>
    <text x="${G.right}" y="${G.height - 6}" text-anchor="end">${t("perfNow")}</text>`;
  const lines = series
    .filter(item => item.points.length)
    .map(item => html`<polyline class="chart-line is-${item.key}" points="${item.points.map(point => `${point.x.toFixed(1)},${point.y.toFixed(1)}`).join(" ")}"/>`);
  const labels = placeEndLabels(series.filter(item => item.points.length).map(item => ({ key: item.key, text: t(item.label), y: item.points.at(-1).y + 4 })));
  return html`<svg viewBox="0 0 ${G.width} ${G.height}" width="${G.width}" height="${G.height}" role="img" aria-label="${t("perfChartLabel")}">
    <g class="chart-grid" aria-hidden="true">${grid}</g>
    <g class="chart-axis" aria-hidden="true">${axis}</g>
    ${lines}
    <g aria-hidden="true">${labels.map(label => html`<text class="chart-end-label" x="${G.right + 6}" y="${label.y.toFixed(1)}">${label.text}</text>`)}</g>
    <g class="chart-hover" aria-hidden="true"></g>
    <rect class="chart-hit" x="${G.left}" y="${G.top}" width="${G.right - G.left}" height="${G.bottom - G.top}"/>
  </svg>`;
}

/** Crosshair + tooltip for the nearest sample under the pointer. */
export function bindChartHover(container, getHistory) {
  const svg = container.querySelector("svg");
  const hit = svg?.querySelector(".chart-hit");
  if (!hit) return;
  const layer = svg.querySelector(".chart-hover");
  let tooltip = container.querySelector(".chart-tooltip");
  if (!tooltip) {
    tooltip = document.createElement("div");
    tooltip.className = "chart-tooltip";
    tooltip.hidden = true;
    container.append(tooltip);
  }
  const hide = () => { layer.innerHTML = ""; tooltip.hidden = true; };
  hit.addEventListener("pointerleave", hide);
  hit.addEventListener("pointermove", event => {
    const now = Date.now();
    const history = getHistory();
    const box = svg.getBoundingClientRect();
    const scale = G.width / box.width;
    const pointerX = (event.clientX - box.left) * scale;
    let nearest = null;
    for (const sample of history) {
      const time = new Date(sample.timestamp).getTime();
      const distance = Math.abs(x(time, now) - pointerX);
      if (!nearest || distance < nearest.distance) nearest = { sample, time, distance };
    }
    if (!nearest) return hide();
    const cx = x(nearest.time, now);
    const values = SERIES.map(series => ({ ...series, v: series.value(nearest.sample) }));
    layer.innerHTML = html`<line class="chart-crosshair" x1="${cx}" x2="${cx}" y1="${G.top}" y2="${G.bottom}"/>
      ${values.filter(item => Number.isFinite(item.v)).map(item => html`<circle class="chart-dot is-${item.key}" cx="${cx}" cy="${y(item.v)}" r="4.5"/>`)}`.value;
    tooltip.innerHTML = html`<strong>${new Date(nearest.time).toLocaleTimeString()}</strong>
      <dl>${values.map(item => html`<dt>${t(item.label)}</dt><dd>${Number.isFinite(item.v) ? formatPercent(item.v) : t("unavailable")}</dd>`)}</dl>`.value;
    tooltip.hidden = false;
    const containerBox = container.getBoundingClientRect();
    const left = cx / scale + box.left - containerBox.left;
    const flip = left > containerBox.width / 2;
    tooltip.style.left = `${flip ? left - tooltip.offsetWidth - 12 : left + 12}px`;
    tooltip.style.top = `${box.top - containerBox.top + 8}px`;
  });
}

export const chartLegend = () =>
  html`<div class="chart-legend" aria-hidden="true">${SERIES.map(series => html`<span><i class="is-${series.key}"></i>${t(series.label)}</span>`)}</div>`;

