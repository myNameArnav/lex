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
  assert.equal(niceStep(0.25), 0.1);   // the hours chart's floor: 0 / 6m / 12m / 18m
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

test('a chart axis uses one unit, the one its top tick needs', async () => {
  const { axisHours, axisBitrate, niceStep } = await loadCharts();
  const ticks = (fmt, max) => [0, 1, 2, 3].map((i) => fmt((max * i) / 3));
  // The hours chart's floor: minutes all the way down, not "0h".
  assert.deepEqual(ticks(axisHours(0.3), 0.3), ['0m', '6m', '12m', '18m']);
  assert.deepEqual(ticks(axisHours(1.5), 1.5), ['0h', '0.5h', '1h', '1.5h']);
  // Network out: "0 Mbps" and "0.5 Mbps" under "1 Mbps", not "0 kbps" / "500 kbps".
  const net = 3 * niceStep(1.5e6);
  assert.deepEqual(ticks(axisBitrate(net), net), ['0 Mbps', '0.5 Mbps', '1 Mbps', '1.5 Mbps']);
  assert.deepEqual(ticks(axisBitrate(150e6), 150e6), ['0 Mbps', '50 Mbps', '100 Mbps', '150 Mbps']);
  assert.deepEqual(ticks(axisBitrate(600e3), 600e3), ['0 kbps', '200 kbps', '400 kbps', '600 kbps']);
});
