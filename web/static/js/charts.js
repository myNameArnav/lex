// Minimal canvas charts for the dashboard. Palette: validated categorical
// dark-mode slots (blue, orange, aqua) against the panel surface.
import { h } from './ui.js';

export const SERIES = ['#3987e5', '#d95926', '#199e70'];
const GRID = '#262a33';
const AXIS_TEXT = '#8a90a0';
const SURFACE = '#14161b';

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

function niceMax(v) {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

function tooltipEl(box) {
  let t = box.querySelector('.ctip');
  if (!t) {
    t = h('div', { class: 'ctip', style: { position: 'absolute', pointerEvents: 'none', background: 'rgba(12,13,16,.96)', border: '1px solid #2b2f39', borderRadius: '8px', padding: '6px 9px', fontSize: '12px', whiteSpace: 'nowrap', zIndex: 5, display: 'none' } });
    box.appendChild(t);
  }
  return t;
}

// lineChart draws one or more series over shared x values (unix seconds).
// opts: { series: [{name, values}], times, height, fmt, max, area }
export function lineChart(box, opts) {
  if (!box.isConnected) { requestAnimationFrame(() => box.isConnected && lineChart(box, opts)); return; }
  box.style.position = 'relative';
  let canvas = box.querySelector('canvas.lc');
  if (!canvas) { canvas = h('canvas', { class: 'lc chart' }); box.prepend(canvas); }
  const height = opts.height || 160;
  const { ctx, w, h: H } = setupCanvas(canvas, height);
  const padL = 46, padR = 8, padT = 8, padB = 20;
  const n = opts.times.length;
  const all = opts.series.flatMap((s) => s.values);
  const max = opts.max || niceMax(Math.max(0, ...all) * 1.1);
  const x = (i) => padL + (n <= 1 ? 0 : (i / (n - 1)) * (w - padL - padR));
  const y = (v) => padT + (1 - Math.min(v, max) / max) * (H - padT - padB);
  // Grid + y labels
  ctx.font = '11px system-ui, sans-serif';
  ctx.fillStyle = AXIS_TEXT;
  ctx.strokeStyle = GRID;
  ctx.lineWidth = 1;
  for (let i = 0; i <= 3; i++) {
    const v = (max * i) / 3, yy = Math.round(y(v)) + 0.5;
    ctx.beginPath(); ctx.moveTo(padL, yy); ctx.lineTo(w - padR, yy); ctx.stroke();
    ctx.textAlign = 'right'; ctx.textBaseline = 'middle';
    ctx.fillText(opts.fmt(v), padL - 6, yy);
  }
  // x labels: start / middle / end
  if (n > 1) {
    ctx.textBaseline = 'alphabetic';
    const lbl = (i) => new Date(opts.times[i] * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    ctx.textAlign = 'left'; ctx.fillText(lbl(0), padL, H - 4);
    ctx.textAlign = 'center'; ctx.fillText(lbl(Math.floor(n / 2)), x(Math.floor(n / 2)), H - 4);
    ctx.textAlign = 'right'; ctx.fillText(lbl(n - 1), w - padR, H - 4);
  }
  opts.series.forEach((s, si) => {
    const color = s.color || SERIES[si];
    if (n < 2) return;
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
    ctx.beginPath(); ctx.arc(lx, ly, 6, 0, Math.PI * 2); ctx.fillStyle = SURFACE; ctx.fill();
    ctx.beginPath(); ctx.arc(lx, ly, 4, 0, Math.PI * 2); ctx.fillStyle = color; ctx.fill();
  });
  // Legend for >= 2 series (line keys, text in text tokens).
  let legend = box.querySelector('.legend');
  if (opts.series.length > 1) {
    if (!legend) { legend = h('div', { class: 'legend' }); box.appendChild(legend); }
    legend.replaceChildren(...opts.series.map((s, si) => h('span', null,
      h('i', { style: { background: s.color || SERIES[si], height: '2px', width: '14px', borderRadius: '2px', verticalAlign: '3px' } }),
      `${s.name} · ${opts.fmt(s.values[n - 1] || 0)}`)));
  }
  // Crosshair tooltip
  const tip = tooltipEl(box);
  canvas.onpointermove = (e) => {
    if (n < 2) return;
    const r = canvas.getBoundingClientRect();
    const px = e.clientX - r.left;
    const i = Math.max(0, Math.min(n - 1, Math.round(((px - padL) / (w - padL - padR)) * (n - 1))));
    lineChart(box, opts); // redraw clean
    const c = canvas.getContext('2d');
    c.strokeStyle = '#6f7686'; c.lineWidth = 1;
    c.beginPath(); c.moveTo(Math.round(x(i)) + 0.5, padT); c.lineTo(Math.round(x(i)) + 0.5, H - padB); c.stroke();
    tip.replaceChildren(
      h('div', { class: 'dim' }, new Date(opts.times[i] * 1000).toLocaleTimeString()),
      ...opts.series.map((s, si) => h('div', { class: 'row', style: { gap: '6px' } },
        h('i', { style: { display: 'inline-block', width: '10px', height: '2px', background: s.color || SERIES[si] } }),
        h('b', null, opts.fmt(s.values[i] || 0)), h('span', { class: 'muted' }, s.name))));
    tip.style.display = 'block';
    const left = x(i) + 12;
    tip.style.left = `${left + tip.offsetWidth > w ? x(i) - tip.offsetWidth - 12 : left}px`;
    tip.style.top = '8px';
    canvas.onpointerleave = () => { tip.style.display = 'none'; lineChart(box, opts); };
  };
}

// columnChart: labels + values, one series. opts: { labels, values, fmt, height, tipLabel }
export function columnChart(box, opts) {
  if (!box.isConnected) { requestAnimationFrame(() => box.isConnected && columnChart(box, opts)); return; }
  box.style.position = 'relative';
  let canvas = box.querySelector('canvas.cc');
  if (!canvas) { canvas = h('canvas', { class: 'cc chart' }); box.prepend(canvas); }
  const height = opts.height || 180;
  const draw = (hover = -1) => {
    const { ctx, w, h: H } = setupCanvas(canvas, height);
    const padL = 40, padR = 6, padT = 10, padB = 22;
    const n = opts.values.length;
    const max = niceMax(Math.max(0, ...opts.values));
    const band = (w - padL - padR) / Math.max(1, n);
    const bw = Math.max(2, Math.min(24, band - 2));
    const y = (v) => padT + (1 - v / max) * (H - padT - padB);
    ctx.font = '11px system-ui, sans-serif';
    ctx.strokeStyle = GRID; ctx.fillStyle = AXIS_TEXT; ctx.lineWidth = 1;
    for (let i = 0; i <= 3; i++) {
      const v = (max * i) / 3, yy = Math.round(y(v)) + 0.5;
      ctx.beginPath(); ctx.moveTo(padL, yy); ctx.lineTo(w - padR, yy); ctx.stroke();
      ctx.textAlign = 'right'; ctx.textBaseline = 'middle'; ctx.fillText(opts.fmt(v), padL - 6, yy);
    }
    opts.values.forEach((v, i) => {
      const cx = padL + band * i + band / 2, top = y(v), base = y(0);
      const hgt = base - top;
      if (hgt <= 0) return;
      ctx.fillStyle = i === hover ? '#5598e7' : SERIES[0];
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
    // Sparse x labels
    ctx.fillStyle = AXIS_TEXT; ctx.textBaseline = 'alphabetic'; ctx.textAlign = 'center';
    const step = Math.ceil(n / Math.max(1, Math.floor((w - padL) / 56)));
    opts.labels.forEach((l, i) => { if (i % step === 0) ctx.fillText(l, padL + band * i + band / 2, H - 5); });
    return { padL, band, w };
  };
  const g = draw();
  const tip = tooltipEl(box);
  canvas.onpointermove = (e) => {
    const r = canvas.getBoundingClientRect();
    const i = Math.floor((e.clientX - r.left - g.padL) / g.band);
    if (i < 0 || i >= opts.values.length) { tip.style.display = 'none'; draw(); return; }
    draw(i);
    tip.replaceChildren(h('b', null, opts.fmt(opts.values[i])), h('span', { class: 'muted' }, ` ${opts.tipLabel || ''} · ${opts.fullLabels ? opts.fullLabels[i] : opts.labels[i]}`));
    tip.style.display = 'block';
    const left = g.padL + g.band * i + g.band / 2 + 10;
    tip.style.left = `${left + tip.offsetWidth > g.w ? left - tip.offsetWidth - 20 : left}px`;
    tip.style.top = '4px';
  };
  canvas.onpointerleave = () => { tip.style.display = 'none'; draw(); };
}

// sparkline for stat tiles: de-emphasis stroke with the latest point accented.
export function sparkline(canvas, values, max) {
  if (!canvas.isConnected) { requestAnimationFrame(() => canvas.isConnected && sparkline(canvas, values, max)); return; }
  const { ctx, w, h: H } = setupCanvas(canvas, 44);
  if (values.length < 2) return;
  const m = max || niceMax(Math.max(...values, 0.0001) * 1.1);
  const x = (i) => (i / (values.length - 1)) * (w - 6) + 1;
  const y = (v) => 3 + (1 - Math.min(v, m) / m) * (H - 6);
  ctx.beginPath();
  values.forEach((v, i) => (i ? ctx.lineTo(x(i), y(v)) : ctx.moveTo(x(i), y(v))));
  ctx.lineTo(x(values.length - 1), H); ctx.lineTo(x(0), H); ctx.closePath();
  ctx.globalAlpha = 0.1; ctx.fillStyle = SERIES[0]; ctx.fill(); ctx.globalAlpha = 1;
  ctx.beginPath();
  values.forEach((v, i) => (i ? ctx.lineTo(x(i), y(v)) : ctx.moveTo(x(i), y(v))));
  ctx.strokeStyle = '#4b5263'; ctx.lineWidth = 1.5; ctx.stroke();
  const lx = x(values.length - 1), ly = y(values[values.length - 1]);
  ctx.beginPath(); ctx.arc(lx, ly, 5, 0, Math.PI * 2); ctx.fillStyle = SURFACE; ctx.fill();
  ctx.beginPath(); ctx.arc(lx, ly, 3, 0, Math.PI * 2); ctx.fillStyle = SERIES[0]; ctx.fill();
}

// barList renders horizontal bars as HTML (label · bar · value).
export function barList(items, fmt, colorFor) {
  const max = Math.max(1, ...items.map((i) => i.value));
  if (!items.length) return h('div', { class: 'dim small' }, 'No data yet');
  return h('div', { class: 'bar-list' }, items.map((it, idx) => h('div', { class: 'bar-item', title: `${it.label}: ${fmt(it.value)}` },
    h('span', { class: 'ellipsis' }, it.label),
    h('span', { class: 'track' }, h('i', { style: { width: `${Math.max(1, (it.value / max) * 100)}%`, background: colorFor ? colorFor(it, idx) : SERIES[0] } })),
    h('span', { class: 'muted nowrap' }, fmt(it.value)))));
}
