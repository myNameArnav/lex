// Subtitle appearance: the choices offered in the player and in Settings,
// the CSS the player's own subtitle overlay is drawn with, and the overrides
// applied to styled (ASS/SSA) dialogue. Pure, so it can be tested in node.

const STACK_SANS = 'system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif';

export const SUB_SIZES = [
  ['xsmall', 'Extra small', 0.65],
  ['small', 'Small', 0.8],
  ['medium', 'Medium', 1],
  ['large', 'Large', 1.3],
  ['xlarge', 'Extra large', 1.6],
  ['huge', 'Huge', 2],
];

export const SUB_POSITIONS = [['low', 'Low'], ['normal', 'Normal'], ['high', 'High']];

export const SUB_FONTS = [
  ['sans', 'Sans-serif', STACK_SANS],
  ['serif', 'Serif', 'Georgia, Cambria, "Times New Roman", Times, serif'],
  ['mono', 'Monospace', 'ui-monospace, "SF Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace'],
  ['rounded', 'Rounded', `ui-rounded, "SF Pro Rounded", "Arial Rounded MT Bold", "Varela Round", "Nunito", ${STACK_SANS}`],
  ['casual', 'Casual', '"Comic Sans MS", "Comic Neue", "Chalkboard SE", "Comic Relief", cursive, sans-serif'],
  ['condensed', 'Condensed', '"Arial Narrow", "Roboto Condensed", "Helvetica Neue Condensed", "Liberation Sans Narrow", sans-serif-condensed, sans-serif'],
  ['smallcaps', 'Small caps', STACK_SANS],
];

export const SUB_COLORS = [
  ['white', 'White', '#ffffff'],
  ['yellow', 'Yellow', '#ffeb3b'],
  ['green', 'Green', '#4cff4c'],
  ['cyan', 'Cyan', '#40e0ff'],
  ['blue', 'Blue', '#4a7dff'],
  ['magenta', 'Magenta', '#ff4dff'],
  ['red', 'Red', '#ff4d4d'],
  ['grey', 'Grey', '#9e9e9e'],
  ['black', 'Black', '#000000'],
];

export const SUB_OPACITIES = [[1, '100%'], [0.75, '75%'], [0.5, '50%'], [0.25, '25%']];
export const SUB_BG_OPACITIES = [[1, '100%'], [0.8, '80%'], [0.6, '60%'], [0.4, '40%'], [0.25, '25%']];

export const SUB_EDGES = [
  ['auto', 'Automatic', 'Outline without a background box'],
  ['none', 'None'],
  ['outline', 'Outline'],
  ['shadow', 'Drop shadow'],
  ['raised', 'Raised'],
  ['depressed', 'Depressed'],
];

export const SUB_WEIGHTS = [['normal', 'Regular'], ['bold', 'Bold']];

// The preferences this module reads, with their defaults (also in prefs.js).
export const SUB_STYLE_DEFAULTS = {
  subSize: 'medium',
  subPos: 'normal',
  subFont: 'sans',
  subWeight: 'bold',
  subColor: 'white',
  subOpacity: 1,
  subBg: false,
  subBgColor: 'black',
  subBgOpacity: 0.6,
  subEdge: 'auto',
  subEdgeColor: 'black',
};

const find = (list, k) => list.find((x) => String(x[0]) === String(k));
export const subLabel = (list, k) => (find(list, k) || [])[1];
const colorOf = (k, fallback) => (find(SUB_COLORS, k) || find(SUB_COLORS, fallback))[2];
const num = (v, d) => (Number.isFinite(+v) && v !== '' && v !== null ? Math.min(1, Math.max(0, +v)) : d);

export function sizeScale(k) { return (find(SUB_SIZES, k) || find(SUB_SIZES, 'medium'))[2]; }

function rgba(hex, a) {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${n >> 16}, ${(n >> 8) & 255}, ${n & 255}, ${+a.toFixed(3)})`;
}

// The edge actually drawn: Automatic outlines text only without a box.
export function effectiveEdge(p) {
  const e = find(SUB_EDGES, p.subEdge) ? p.subEdge : 'auto';
  return e === 'auto' ? (p.subBg ? 'none' : 'outline') : e;
}

function shadow(edge, c) {
  switch (edge) {
    case 'outline': return `-.05em -.05em 0 ${c}, .05em -.05em 0 ${c}, -.05em .05em 0 ${c}, .05em .05em 0 ${c}, 0 0 .12em ${c}`;
    case 'shadow': return `.06em .06em .1em ${c}, .03em .03em .05em ${c}`;
    case 'raised': return `.03em .03em 0 ${c}, .06em .06em 0 ${c}`;
    case 'depressed': return `-.03em -.03em 0 ${c}, -.06em -.06em 0 ${c}`;
    default: return 'none';
  }
}

// CSS custom properties for the player's overlay (and the Settings preview).
export function subStyleVars(prefs) {
  const p = { ...SUB_STYLE_DEFAULTS, ...prefs };
  const font = find(SUB_FONTS, p.subFont) || SUB_FONTS[0];
  const bg = p.subBg ? rgba(colorOf(p.subBgColor, 'black'), num(p.subBgOpacity, 0.6)) : 'transparent';
  return {
    '--sub-scale': String(sizeScale(p.subSize)),
    '--sub-font': font[2],
    '--sub-variant': font[0] === 'smallcaps' ? 'small-caps' : 'normal',
    '--sub-weight': p.subWeight === 'normal' ? '400' : '600',
    '--sub-color': rgba(colorOf(p.subColor, 'white'), num(p.subOpacity, 1)),
    '--sub-bg': bg,
    '--sub-shadow': shadow(effectiveEdge(p), colorOf(p.subEdgeColor, 'black')),
  };
}

// ASS colours are &HAABBGGRR, with alpha 00 opaque.
export function assColor(hex, opacity = 1) {
  const n = parseInt(hex.slice(1), 16);
  const hx = (v) => v.toString(16).toUpperCase().padStart(2, '0');
  return `&H${hx(Math.round((1 - num(opacity, 1)) * 255))}${hx(n & 255)}${hx((n >> 8) & 255)}${hx(n >> 16)}`;
}

// What to change in a bottom-aligned ASS dialogue style. Fonts stay the
// release's (the renderer can't reach the system's), and choices left at
// their defaults keep the release's look.
// fields: the style's current values by lowercase Format name; resY: PlayResY.
export function assStyleOverrides(prefs, fields, resY) {
  const p = { ...SUB_STYLE_DEFAULTS, ...prefs };
  const out = {};
  out.fontsize = Math.round(+fields.fontsize * sizeScale(p.subSize) * 10) / 10;
  const mv = +fields.marginv || Math.round(resY * 0.04);
  out.marginv = Math.round(p.subPos === 'low' ? mv * 0.4 : p.subPos === 'high' ? mv + resY * 0.1 : mv);
  if (p.subColor !== 'white' || num(p.subOpacity, 1) !== 1) {
    out.primarycolour = assColor(colorOf(p.subColor, 'white'), p.subOpacity);
  }
  if (p.subWeight === 'normal' && 'bold' in fields) out.bold = 0;
  const unit = Math.max(1, Math.round(resY / 150));
  const edgeC = assColor(colorOf(p.subEdgeColor, 'black'));
  if (p.subBg) {
    // BorderStyle 3 draws an opaque box in the outline colour.
    out.borderstyle = 3;
    out.outlinecolour = assColor(colorOf(p.subBgColor, 'black'), num(p.subBgOpacity, 0.6));
    out.outline = unit;
    out.shadow = 0;
    return out;
  }
  switch (p.subEdge) {
    case 'none': out.outline = 0; out.shadow = 0; break;
    case 'outline': out.borderstyle = 1; out.outline = unit; out.shadow = 0; out.outlinecolour = edgeC; break;
    case 'shadow': case 'raised':
      out.borderstyle = 1; out.outline = 0; out.shadow = unit * (p.subEdge === 'shadow' ? 2 : 1); out.backcolour = edgeC; break;
    case 'depressed':
      // ASS shadows only fall down-right: a thin outline reads closest.
      out.borderstyle = 1; out.outline = Math.max(1, unit / 2); out.shadow = 0; out.outlinecolour = edgeC; break;
    default:
      // Automatic: the release's own outline, in the chosen colour if one was picked.
      if (p.subEdgeColor !== 'black') out.outlinecolour = edgeC;
  }
  return out;
}
