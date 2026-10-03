// Minimal canvas charts for the dashboard. Series palette: validated
// categorical dark-mode slots (blue, orange, aqua) against the panel surface;
// grid, axis and surface colours come from the CSS theme.
import { h, icons } from './ui.js';

export const SERIES = ['#3987e5', '#d95926', '#199e70'];
const SERIES_HOVER = '#5598e7';

function theme() {
  const s = getComputedStyle(document.documentElement);
  const v = (name) => s.getPropertyValue(name).trim();
  return { grid: v('--bg4'), axis: v('--text3'), surface: v('--bg2'), cross: v('--line-strong') };
}

function setupCanvas(canvas, height) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth || canvas.parentElement.clientWidth || 300;
  canvas.width = Math.round(w * dpr);
  canvas.height = Math.round(height * dpr);
  canvas.style.height = `${height}px`;
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, height);
  return { ctx, w, h: height };
}

// niceStep picks a round gridline step (1, 2, 2.5 or 5 × 10^n; whole numbers
// for counts) so that `ticks` steps reach max.
export function niceStep(max, ticks = 3, integer = false) {
  const raw = max / ticks;
  if (!(raw > 0)) return 1;
  const p = 10 ** Math.floor(Math.log10(raw));
  const step = (integer ? [1, 2, 5, 10] : [1, 2, 2.5, 5, 10]).map((m) => m * p).find((s) => s >= raw * (1 - 1e-9));
  return integer ? Math.max(1, Math.ceil(step)) : step;
}

// Axis labels drop a trailing ".0" ("1.0 Mbps" reads as "1 Mbps").
const axisLabel = (fmt, v) => fmt(v).replace(/(\d)\.0(?!\d)/g, '$1');

function yAxis(ctx, t, fmt, max, ticks, padL, padR, w, y) {
  ctx.font = '11px system-ui, sans-serif';
  ctx.strokeStyle = t.grid; ctx.fillStyle = t.axis; ctx.lineWidth = 1;
  ctx.textAlign = 'right'; ctx.textBaseline = 'middle';
  for (let i = 0; i <= ticks; i++) {
    const v = (max * i) / ticks, yy = Math.round(y(v)) + 0.5;
    ctx.beginPath(); ctx.moveTo(padL, yy); ctx.lineTo(w - padR, yy); ctx.stroke();
    ctx.fillText(axisLabel(fmt, v), padL - 6, yy);
  }
}

function labelWidth(ctx, fmt, max, ticks) {
  ctx.font = '11px system-ui, sans-serif';
  let wMax = 0;
  for (let i = 0; i <= ticks; i++) wMax = Math.max(wMax, ctx.measureText(axisLabel(fmt, (max * i) / ticks)).width);
  return Math.ceil(wMax) + 12;
}

// mount gives box one canvas (role=img, named by label) with a read-out
// tooltip for mouse hover, touch (tap, or drag sideways) and keyboard (arrow
// keys, Home/End, Escape), and repaints it when the box changes width.
// draw(canvas, pick) paints and returns { n, w, at(px), cx(i), tip(i) };
// pick(geometry) gives the index to highlight (-1 for none).
function mount(box, cls, label, draw) {
  if (!box.isConnected) { requestAnimationFrame(() => box.isConnected && mount(box, cls, label, draw)); return; }
  const c = box._chart ||= setup(box, cls);
  c.draw = draw;
  c.canvas.setAttribute('aria-label', label);
  render(c);
}

function render(c) {
  // A fresh data draw keeps the read-out on the same spot (pointer) or index
  // (keyboard), so a polled chart never shows a stale tooltip.
  c.g = c.draw(c.canvas, (g) => (c.i = c.px != null ? g.at(c.px) : Math.min(c.i, g.n - 1)));
  const { tip, g } = c;
  if (c.i < 0) { tip.hidden = true; return; }
  tip.replaceChildren(...g.tip(c.i));
  tip.hidden = false;
  const x = g.cx(c.i), left = x + 12;
  tip.style.left = `${left + tip.offsetWidth > g.w ? Math.max(0, x - tip.offsetWidth - 12) : left}px`;
}

function setup(box, cls) {
  box.style.position = 'relative';
  const canvas = h('canvas', { class: `${cls} chart`, role: 'img', tabindex: '0' });
  const tip = h('div', { class: 'ctip', hidden: true });
  box.prepend(canvas);
  box.append(tip);
  const c = { canvas, tip, i: -1, px: null };
  const pos = (e) => e.clientX - canvas.getBoundingClientRect().left;
  const outside = (e) => { if (!box.contains(e.target)) hide(); };
  const hide = () => {
    document.removeEventListener('pointerdown', outside, true);
    if (c.i < 0 && c.px == null) return;
    c.i = -1; c.px = null; render(c);
  };
  canvas.onpointerdown = (e) => {
    c.px = pos(e); render(c);
    // Touch has no hover: the read-out stays until a tap elsewhere.
    if (e.pointerType !== 'mouse') document.addEventListener('pointerdown', outside, true);
  };
  canvas.onpointermove = (e) => { if (e.pointerType === 'mouse' || e.buttons) { c.px = pos(e); render(c); } };
  canvas.onpointerleave = (e) => { if (e.pointerType === 'mouse') hide(); };
  const steps = { ArrowLeft: -1, ArrowRight: 1, Home: -Infinity, End: Infinity };
  canvas.onkeydown = (e) => {
    const n = c.g?.n || 0;
    if (e.key === 'Escape' && c.i >= 0) { e.stopPropagation(); hide(); return; }
    if (!(e.key in steps) || !n) return;
    e.preventDefault();
    const d = steps[e.key] * (c.g.step || 1), from = c.i < 0 ? (d > 0 ? -1 : n) : c.i;
    c.px = null;
    c.i = Math.max(0, Math.min(n - 1, from + d));
    render(c);
  };
  canvas.onblur = () => { if (c.px == null) hide(); };
  let w = box.clientWidth;
  new ResizeObserver(() => { if (box.clientWidth !== w) { w = box.clientWidth; render(c); } }).observe(box);
  return c;
}

// lineChart draws one or more series over shared x values (unix seconds).
// opts: { title, series: [{name, values, color?}], times, height, fmt,
// max?, floor? (smallest axis top), ticks?, area? }
export function lineChart(box, opts) {
  const n = opts.times.length;
  const ticks = opts.ticks || 3;
  const max = opts.max || ticks * niceStep(Math.max(opts.floor || 0, ...opts.series.flatMap((s) => s.values)), ticks);
  const height = opts.height || 160;
  const latest = (s) => opts.fmt(s.values[n - 1] || 0);
  // Legend for >= 2 series (line keys, text in text tokens).
  let legend = box.querySelector('.legend');
  if (opts.series.length > 1) {
    if (!legend) { legend = h('div', { class: 'legend', 'aria-hidden': 'true' }); box.appendChild(legend); }
    legend.replaceChildren(...opts.series.map((s, si) => h('span', null,
      h('i', { style: { background: s.color || SERIES[si], height: '2px', width: '14px', borderRadius: '2px', verticalAlign: '3px' } }),
      `${s.name} · ${latest(s)}`)));
  }
  const span = n > 1 ? opts.times[n - 1] - opts.times[0] : 0;
  const time = (i, secs) => new Date(opts.times[i] * 1000).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit', second: secs ? '2-digit' : undefined });
  const label = `${opts.title}: ${opts.series.map((s) => `${s.name} ${latest(s)}`).join(', ')}`;
  mount(box, 'lc', label, (canvas, pick) => {
    const t = theme();
    const { ctx, w, h: H } = setupCanvas(canvas, height);
    const padR = 8, padT = 8, padB = 20;
    const padL = labelWidth(ctx, opts.fmt, max, ticks);
    const x = (i) => padL + (n <= 1 ? 0 : (i / (n - 1)) * (w - padL - padR));
    const y = (v) => padT + (1 - Math.min(v, max) / max) * (H - padT - padB);
    const g = {
      n: n > 1 ? n : 0, w, cx: x, step: Math.ceil(n / 40),
      at: (px) => (n < 2 ? -1 : Math.max(0, Math.min(n - 1, Math.round(((px - padL) / (w - padL - padR)) * (n - 1))))),
      tip: (i) => [h('div', { class: 'dim' }, time(i, true)),
        ...opts.series.map((s, si) => h('div', { class: 'row', style: { gap: '6px' } },
          h('i', { style: { display: 'inline-block', width: '10px', height: '2px', background: s.color || SERIES[si] } }),
          h('b', null, opts.fmt(s.values[i] || 0)), h('span', { class: 'muted' }, s.name)))],
    };
    const hover = pick(g);
    yAxis(ctx, t, opts.fmt, max, ticks, padL, padR, w, y);
    // x labels: start / middle / end, with seconds while the history is short.
    if (n > 1) {
      const secs = span < 180;
      ctx.textBaseline = 'alphabetic';
      const ends = [time(0, secs), time(n - 1, secs)], mid = time(Math.floor(n / 2), secs);
      ctx.textAlign = 'left'; ctx.fillText(ends[0], padL, H - 4);
      ctx.textAlign = 'right'; ctx.fillText(ends[1], w - padR, H - 4);
      // The middle label only where it can't run into the ends.
      const need = Math.max(...ends.map((l) => ctx.measureText(l).width)) * 2 + ctx.measureText(mid).width + 24;
      if (need <= w - padL - padR) { ctx.textAlign = 'center'; ctx.fillText(mid, x(Math.floor(n / 2)), H - 4); }
    }
    if (n < 2) return g;
    opts.series.forEach((s, si) => {
      const color = s.color || SERIES[si];
      if (opts.series.length === 1 || opts.area) {
        ctx.beginPath();
        s.values.forEach((v, i) => (i ? ctx.lineTo(x(i), y(v)) : ctx.moveTo(x(i), y(v))));
        ctx.lineTo(x(n - 1), y(0)); ctx.lineTo(x(0), y(0)); ctx.closePath();
        ctx.globalAlpha = 0.1; ctx.fillStyle = color; ctx.fill(); ctx.globalAlpha = 1;
      }
      ctx.beginPath();
      s.values.forEach((v, i) => (i ? ctx.lineTo(x(i), y(v)) : ctx.moveTo(x(i), y(v))));
      ctx.strokeStyle = color; ctx.lineWidth = 2; ctx.lineJoin = 'round'; ctx.lineCap = 'round'; ctx.stroke();
      // End marker with surface ring.
      const lx = x(n - 1), ly = y(s.values[n - 1]);
      ctx.beginPath(); ctx.arc(lx, ly, 6, 0, Math.PI * 2); ctx.fillStyle = t.surface; ctx.fill();
      ctx.beginPath(); ctx.arc(lx, ly, 4, 0, Math.PI * 2); ctx.fillStyle = color; ctx.fill();
    });
    if (hover >= 0) {
      const xx = Math.round(x(hover)) + 0.5;
      ctx.strokeStyle = t.cross; ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(xx, padT); ctx.lineTo(xx, H - padB); ctx.stroke();
    }
    return g;
  });
}

// columnChart: labels + values, one series. opts: { title, labels, values,
// fmt, height, tipLabel, fullLabels?, floor? (smallest axis top) }. Its
// accessible name sums it up.
export function columnChart(box, opts) {
  const n = opts.values.length;
  const full = opts.fullLabels || opts.labels;
  const ticks = 3;
  const counts = opts.values.every(Number.isInteger);
  const top = Math.max(0, ...opts.values);
  const max = ticks * niceStep(Math.max(top, opts.floor || 0), ticks, counts);
  const sum = opts.values.reduce((a, b) => a + b, 0);
  const peak = opts.values.indexOf(top);
  const label = sum ? `${opts.title}. Total ${opts.fmt(sum)}; highest ${opts.fmt(top)} (${full[peak]}).` : `${opts.title}. No data.`;
  const height = opts.height || 180;
  mount(box, 'cc', label, (canvas, pick) => {
    const t = theme();
    const { ctx, w, h: H } = setupCanvas(canvas, height);
    const padR = 6, padT = 10, padB = 22;
    const padL = labelWidth(ctx, opts.fmt, max, ticks);
    const band = (w - padL - padR) / Math.max(1, n);
    const bw = Math.max(2, Math.min(24, band - 2));
    const y = (v) => padT + (1 - v / max) * (H - padT - padB);
    const g = {
      n, w,
      at: (px) => { const i = Math.floor((px - padL) / band); return i >= 0 && i < n ? i : -1; },
      cx: (i) => padL + band * i + band / 2,
      tip: (i) => [h('b', null, opts.fmt(opts.values[i])), h('span', { class: 'muted' }, ` ${opts.tipLabel || ''} · ${full[i]}`)],
    };
    const hover = pick(g);
    yAxis(ctx, t, opts.fmt, max, ticks, padL, padR, w, y);
    opts.values.forEach((v, i) => {
      const cx = g.cx(i), top = y(v), base = y(0);
      const hgt = base - top;
      if (hgt <= 0) return;
      ctx.fillStyle = i === hover ? SERIES_HOVER : SERIES[0];
      const r = Math.min(4, bw / 2, hgt);
      ctx.beginPath();
      ctx.moveTo(cx - bw / 2, base);
      ctx.lineTo(cx - bw / 2, top + r);
      ctx.quadraticCurveTo(cx - bw / 2, top, cx - bw / 2 + r, top);
      ctx.lineTo(cx + bw / 2 - r, top);
      ctx.quadraticCurveTo(cx + bw / 2, top, cx + bw / 2, top + r);
      ctx.lineTo(cx + bw / 2, base);
      ctx.closePath(); ctx.fill();
    });
    if (hover >= 0 && !(opts.values[hover] > 0)) {
      // An empty column still shows where the read-out points.
      ctx.fillStyle = t.cross; ctx.fillRect(g.cx(hover) - bw / 2, y(0) - 2, bw, 2);
    }
    // Sparse x labels
    ctx.fillStyle = t.axis; ctx.textBaseline = 'alphabetic'; ctx.textAlign = 'center';
    const step = Math.ceil(n / Math.max(1, Math.floor((w - padL) / 56)));
    opts.labels.forEach((l, i) => { if (i % step === 0) ctx.fillText(l, g.cx(i), H - 5); });
    return g;
  });
}

// barList: labelled horizontal bars (label · bar · value). Items with onClick
// become buttons with a chevron; wrap lets long labels wrap instead of
// truncating.
export function barList(items, fmt, colorFor, { wrap = false } = {}) {
  if (!items.length) return h('div', { class: 'dim small' }, 'No data yet');
  const max = Math.max(1, ...items.map((i) => i.value));
  return h('div', { class: 'bar-list' }, items.map((it, idx) => h(it.onClick ? 'button' : 'div', {
    class: `bar-item${it.onClick ? ' link' : ''}${wrap ? ' wrap' : ''}`, type: it.onClick ? 'button' : null,
    title: `${it.label}: ${fmt(it.value)}`, onclick: it.onClick || null,
  },
  h('span', { class: 'ellipsis' }, it.label),
  h('span', { class: 'track' }, h('i', { style: { width: `${Math.max(1, (it.value / max) * 100)}%`, background: colorFor ? colorFor(it, idx) : SERIES[0] } })),
  h('span', { class: 'muted nowrap' }, fmt(it.value)),
  it.onClick ? h('span', { class: 'chev', 'aria-hidden': 'true', html: icons.chevR }) : null)));
}
