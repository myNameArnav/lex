import assert from 'node:assert/strict';
import test from 'node:test';
import { DEFAULTS } from '../static/js/prefs.js';
import { SUB_STYLE_DEFAULTS, subStyleVars, assColor, assStyleOverrides, effectiveEdge, sizeScale } from '../static/js/substyle.js';

test('subtitle style defaults match the stored preference defaults', () => {
  for (const [k, v] of Object.entries(SUB_STYLE_DEFAULTS)) assert.equal(DEFAULTS[k], v, k);
});

test('the default look is the old one: white semi-bold text on a 60% black box, no edge', () => {
  const v = subStyleVars({});
  assert.equal(v['--sub-scale'], '1');
  assert.equal(v['--sub-weight'], '600');
  assert.equal(v['--sub-color'], 'rgba(255, 255, 255, 1)');
  assert.equal(v['--sub-bg'], 'rgba(0, 0, 0, 0.6)');
  assert.equal(v['--sub-shadow'], 'none');
});

test('without a box, Automatic outlines the text; colours and opacity follow the choices', () => {
  const v = subStyleVars({ subBg: false, subColor: 'yellow', subOpacity: 0.5, subEdgeColor: 'blue', subFont: 'smallcaps', subWeight: 'normal', subSize: 'huge' });
  assert.equal(v['--sub-bg'], 'transparent');
  assert.match(v['--sub-shadow'], /#4a7dff/);
  assert.equal(v['--sub-color'], 'rgba(255, 235, 59, 0.5)');
  assert.equal(v['--sub-variant'], 'small-caps');
  assert.equal(v['--sub-weight'], '400');
  assert.equal(v['--sub-scale'], '2');
  assert.equal(effectiveEdge({ subBg: true, subEdge: 'auto' }), 'none');
  assert.equal(effectiveEdge({ subBg: true, subEdge: 'shadow' }), 'shadow');
});

test('unknown stored values fall back to defaults', () => {
  assert.equal(sizeScale('gigantic'), 1);
  const v = subStyleVars({ subColor: 'chartreuse', subBgOpacity: 'x', subEdge: 'glow' });
  assert.equal(v['--sub-color'], 'rgba(255, 255, 255, 1)');
  assert.equal(v['--sub-bg'], 'rgba(0, 0, 0, 0.6)');
  assert.equal(v['--sub-shadow'], 'none');
});

test('ASS colours are &HAABBGGRR with 00 opaque', () => {
  assert.equal(assColor('#ffeb3b'), '&H003BEBFF');
  assert.equal(assColor('#000000', 0.6), '&H66000000');
  assert.equal(assColor('#ffffff', 0), '&HFFFFFFFF');
});

test('ASS dialogue keeps the release look at defaults, apart from the box', () => {
  const fields = { fontsize: '20', marginv: '10', bold: '-1', primarycolour: '&H00FFFFFF' };
  const plain = assStyleOverrides({ ...SUB_STYLE_DEFAULTS, subBg: false }, fields, 288);
  assert.deepEqual(plain, { fontsize: 20, marginv: 10 });
  const boxed = assStyleOverrides(SUB_STYLE_DEFAULTS, fields, 288);
  assert.equal(boxed.borderstyle, 3);
  assert.equal(boxed.outlinecolour, '&H66000000');
  assert.equal(boxed.primarycolour, undefined);
});

test('ASS dialogue follows chosen colour, weight, size and edge', () => {
  const fields = { fontsize: '20', marginv: '10', bold: '-1' };
  const o = assStyleOverrides({ ...SUB_STYLE_DEFAULTS, subBg: false, subColor: 'yellow', subWeight: 'normal', subSize: 'large', subEdge: 'shadow', subEdgeColor: 'red', subPos: 'high' }, fields, 288);
  assert.equal(o.fontsize, 26);
  assert.equal(o.primarycolour, '&H003BEBFF');
  assert.equal(o.bold, 0);
  assert.equal(o.outline, 0);
  assert.equal(o.shadow, 4);
  assert.equal(o.backcolour, '&H004D4DFF');
  assert.equal(o.marginv, 39);
  const none = assStyleOverrides({ ...SUB_STYLE_DEFAULTS, subBg: false, subEdge: 'none' }, fields, 288);
  assert.equal(none.outline, 0);
  assert.equal(none.shadow, 0);
});
