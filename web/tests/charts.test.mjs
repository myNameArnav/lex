import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

async function loadCharts() {
  const context = vm.createContext({});
  const source = await readFile(new URL('../static/js/charts.js', import.meta.url), 'utf8');
  const module = new vm.SourceTextModule(source, { context });
  await module.link(() => new vm.SyntheticModule(['h', 'icons'], function () {
    this.setExport('h', () => ({}));
    this.setExport('icons', {});
  }, { context }));
  await module.evaluate();
  return module.namespace;
}

test('chart gridlines land on round steps', async () => {
  const { niceStep } = await loadCharts();
  // Three steps reaching at least the data maximum.
  assert.equal(niceStep(10), 5);       // 0 / 5 / 10 / 15 h
  assert.equal(niceStep(6), 2);        // 0 / 2 / 4 / 6
  assert.equal(niceStep(0.5), 0.2);    // 0 / 12m / 24m / 36m
  assert.equal(niceStep(1.5e6), 5e5);  // 0 / 500 kbps / 1 Mbps / 1.5 Mbps
  assert.equal(niceStep(700e6), 2.5e8);
  // An all-zero series still gets a usable scale.
  assert.equal(niceStep(0), 1);
  // Counts stay on whole numbers and skip 2.5.
  assert.equal(niceStep(7, 3, true), 5);
  assert.equal(niceStep(1, 3, true), 1);
  assert.equal(niceStep(14, 3, true), 5);
  for (const max of [0.3, 3, 7.7, 42, 999, 12345]) assert.ok(3 * niceStep(max) >= max, `covers ${max}`);
});
