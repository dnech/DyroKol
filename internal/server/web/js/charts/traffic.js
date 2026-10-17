// Throughput over time: inbound and outbound as two lines with a light area
// wash, a hover crosshair with tooltip, a legend and a table view.
import { html, useMemo, useRef, useState } from "../lib/html.js";
import { rate, time } from "../lib/format.js";
import { useWidth } from "../lib/dom.js";
import { Button } from "../ui/kit.js";

const SERIES = [
  { key: "in", label: "Inbound", hint: "visitor → target", color: "var(--series-1)" },
  { key: "out", label: "Outbound", hint: "target → visitor", color: "var(--series-2)" },
];

const H = 240;
const M = { top: 14, right: 84, bottom: 26, left: 64 };

// bucketize turns per-flush samples into per-bucket throughput (bytes/s).
export function bucketize(samples, now, windowSec, bucketSec) {
  const end = Math.floor(now / bucketSec) * bucketSec;
  const start = end - windowSec + bucketSec;
  const n = Math.round(windowSec / bucketSec);
  const out = Array.from({ length: n }, (_, i) => ({ t: start + i * bucketSec, in: 0, out: 0 }));
  for (const s of samples) {
    const i = Math.floor((s.ts - start) / bucketSec);
    if (i >= 0 && i < n) {
      out[i].in += s.bytes_in;
      out[i].out += s.bytes_out;
    }
  }
  for (const b of out) { b.in /= bucketSec; b.out /= bucketSec; }
  return out;
}

function niceMax(v) {
  if (v <= 0) return 1024;
  const exp = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * exp >= v) return m * exp;
  return 10 * exp;
}

export function TrafficChart({ points }) {
  const box = useRef();
  const width = useWidth(box);
  const [hover, setHover] = useState(null);
  const [table, setTable] = useState(false);

  const geo = useMemo(() => {
    if (!width || points.length < 2) return null;
    const pw = width - M.left - M.right;
    const ph = H - M.top - M.bottom;
    const max = niceMax(Math.max(...points.map((p) => Math.max(p.in, p.out))));
    const x = (i) => M.left + (i / (points.length - 1)) * pw;
    const y = (v) => M.top + ph - (v / max) * ph;
    const line = (k) => points.map((p, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(p[k]).toFixed(1)}`).join("");
    const area = (k) => `${line(k)}L${x(points.length - 1)},${y(0)}L${x(0)},${y(0)}Z`;
    const yTicks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * max);
    // keep ~90px between time labels whatever the width
    const step = [600, 900, 1800, 3600].find((st) => (pw / points.length) * (st / 60) >= 90) || 3600;
    const xTicks = points.map((p, i) => ({ i, t: p.t })).filter(({ t }) => t % step === 0);
    return { pw, ph, max, x, y, line, area, yTicks, xTicks };
  }, [width, points]);

  const last = points[points.length - 1];
  const total = points.reduce((a, p) => a + p.in + p.out, 0);

  // End labels only when they do not collide; the legend always carries identity.
  const endLabels = geo && last && Math.abs(geo.y(last.in) - geo.y(last.out)) >= 16;

  const onMove = (e) => {
    if (!geo) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - rect.left;
    const i = Math.round(((px - M.left) / geo.pw) * (points.length - 1));
    setHover(i >= 0 && i < points.length ? i : null);
  };

  return html`<div class="chart" ref=${box}>
    <div class="chart-bar">
      <div class="legend">
        ${SERIES.map((s) => html`<span class="legend-item">
          <span class="legend-key" style=${{ background: s.color }} />${s.label}
          <span class="legend-hint">${s.hint}</span>
        </span>`)}
      </div>
      <${Button} size="sm" variant="ghost" onClick=${() => setTable(!table)}>${table ? "Chart" : "Table"}<//>
    </div>
    ${table ? html`<${ChartTable} points=${points} />` : html`
    <div class="chart-plot" style=${{ height: `${H}px` }}>
      ${geo && html`<svg width=${width} height=${H} role="img"
          aria-label="Throughput over the last hour, inbound and outbound"
          onPointerMove=${onMove} onPointerLeave=${() => setHover(null)}>
        ${geo.yTicks.map((v) => html`
          <line class="grid" x1=${M.left} x2=${M.left + geo.pw} y1=${geo.y(v)} y2=${geo.y(v)} />
          <text class="tick" x=${M.left - 8} y=${geo.y(v)} dy="0.32em" text-anchor="end">${v ? rate(v) : "0"}</text>`)}
        ${geo.xTicks.map(({ i, t }) => html`
          <text class="tick" x=${geo.x(i)} y=${H - 6} text-anchor="middle">${time(t)}</text>`)}
        ${SERIES.map((s) => html`<path d=${geo.area(s.key)} fill=${s.color} fill-opacity="0.1" stroke="none" />`)}
        ${SERIES.map((s) => html`<path d=${geo.line(s.key)} fill="none" stroke=${s.color} stroke-width="2"
          stroke-linejoin="round" stroke-linecap="round" />`)}
        ${SERIES.map((s) => html`<circle class="mark-dot" cx=${geo.x(points.length - 1)} cy=${geo.y(last[s.key])} r="4" fill=${s.color} />`)}
        ${endLabels && SERIES.map((s) => html`
          <text class="end-label" x=${geo.x(points.length - 1) + 10} y=${geo.y(last[s.key])} dy="0.32em">${rate(last[s.key])}</text>`)}
        ${hover != null && html`<g>
          <line class="crosshair" x1=${geo.x(hover)} x2=${geo.x(hover)} y1=${M.top} y2=${M.top + geo.ph} />
          ${SERIES.map((s) => html`<circle class="mark-dot" cx=${geo.x(hover)} cy=${geo.y(points[hover][s.key])} r="4" fill=${s.color} />`)}
        </g>`}
      </svg>`}
      ${geo && hover != null && html`<div class="tooltip" style=${{
        left: `${Math.min(geo.x(hover) + 12, width - 190)}px`, top: `${M.top}px`,
      }}>
        <div class="tooltip-title">${time(points[hover].t)}</div>
        ${SERIES.map((s) => html`<div class="tooltip-row">
          <span class="legend-key" style=${{ background: s.color }} />${s.label}
          <strong>${rate(points[hover][s.key])}</strong>
        </div>`)}
      </div>`}
      ${geo && total === 0 && html`<div class="chart-empty">No traffic in this period</div>`}
    </div>`}
  </div>`;
}

function ChartTable({ points }) {
  const rows = points.filter((p) => p.in || p.out).reverse();
  if (!rows.length) return html`<div class="chart-empty static">No traffic in this period</div>`;
  return html`<div class="table-wrap chart-table"><table>
    <thead><tr><th>Minute</th><th class="num">Inbound</th><th class="num">Outbound</th></tr></thead>
    <tbody>${rows.map((p) => html`<tr>
      <td>${time(p.t)}</td><td class="num">${rate(p.in)}</td><td class="num">${rate(p.out)}</td>
    </tr>`)}</tbody>
  </table></div>`;
}
