import test from 'node:test';
import assert from 'node:assert/strict';
import { DAY } from '../../scripts/initial-priority.mjs';
import { sampleWindowStarts } from './windows.mjs';

const start = Date.parse('2026-05-23T00:00:00+08:00');
test('120 future days cover early, middle and late continuous windows with a frozen training boundary', () => {
  assert.deepEqual(sampleWindowStarts(start, start + 120 * DAY, 60).map(t => (t - start) / DAY), [0, 15, 30, 45, 60]);
  assert.deepEqual(sampleWindowStarts(start, start + 120 * DAY, 30).map(t => (t - start) / DAY), [0, 23, 45, 68, 90]);
});
test('the actual 320-day training boundary leaves 156 days, including the latest complete test window', () => {
  const end = Date.parse('2026-10-26T00:00:00+08:00');
  for (const horizon of [30, 45, 60]) {
    const starts = sampleWindowStarts(start, end, horizon);
    assert.equal(starts.length, 5);
    assert.equal(starts[0], start);
    assert.equal(starts.at(-1) + horizon * DAY, end);
    const gaps = starts.slice(1).map((t, i) => (t - starts[i]) / DAY);
    assert.ok(Math.max(...gaps) - Math.min(...gaps) <= 1);
  }
});
test('insufficient future is excluded and repeated rounded starts are not counted twice', () => {
  assert.deepEqual(sampleWindowStarts(start, start + 29 * DAY, 30), []);
  assert.deepEqual(sampleWindowStarts(start, start + 30 * DAY, 30), [start]);
  assert.deepEqual(sampleWindowStarts(start, start + 32 * DAY, 30), [start, start + DAY, start + 2 * DAY]);
});
