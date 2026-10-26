import test from 'node:test';
import assert from 'node:assert/strict';
import { DAY, fairScores, attributePeople, observationsBefore, normalizedPrior } from './initial-priority.mjs';
import { buildPrior } from './build-initial-priority.mjs';

const now = Date.parse('2026-10-01T12:00:00+08:00');
const close = (a, b) => assert.ok(Math.abs(a - b) < 1e-12, `${a} != ${b}`);

test('five active days give equal total weight despite 100x request volume', () => {
  const events = [5, 500].flatMap((n, i) => Array.from({ length: n }, (_, j) => ({ id: i + 1, person: String(i), t: now - j % 5 * DAY })));
  for (const h of [7, 30, 60, 365, null]) {
    const { ranking, summary } = fairScores(events, h);
    ranking.forEach(s => close(s.score, 1));
    close(summary.totalPersonWeight, 2);
  }
});

test('active dates use Shanghai midnight; repeated requests do not buy extra days', () => {
  const beforeMidnight = Date.parse('2026-10-01T23:59:59+08:00');
  const one = fairScores(Array.from({ length: 100 }, () => ({ id: 1, person: 'a', t: beforeMidnight })), 60);
  close(one.ranking[0].score, 0.2);
  const two = fairScores([{ id: 1, person: 'a', t: beforeMidnight }, { id: 1, person: 'a', t: beforeMidnight + 2000 }], 60);
  close(two.ranking[0].score, 0.4);
});

test('half-life changes a person’s internal preferences, including old inactive people', () => {
  for (const age of [0, 10000]) {
    const r = fairScores([{ id: 1, person: 'a', t: now - age * DAY }, { id: 2, person: 'a', t: now - (age + 60) * DAY }], 60);
    close(r.ranking[0].score / r.ranking[1].score, 2);
    close(r.ranking.reduce((n, x) => n + x.score, 0), 0.4);
  }
});

test('unknown observations share one capped pool, and prior is below one local demand', () => {
  const r = fairScores(Array.from({ length: 1000 }, () => ({ id: 1, person: null, t: now })), 60);
  close(r.ranking[0].score, 0.25);
  assert.deepEqual(normalizedPrior(r.ranking), [{ songId: 1, score: 0.5 }]);
});

test('future aliases and stronger attribution cannot rewrite training identities', () => {
  const events = [{ id: 1, t: now - 1000 }];
  const old = { ...events[0], name: 'alias', uid: null, priority: 1, random: false };
  const future = { id: 1, t: now + 1000, name: 'alias', uid: 'new-id', priority: 3, random: false };
  assert.deepEqual(attributePeople(events, [old, future], now), attributePeople(events, [old], now));
  assert.equal(attributePeople(events, [old, future], now).events[0].person, 'name:alias');
});

test('future sources do not suppress pre-cutoff fallback or supplemental demand', () => {
  const data = { vrcx: [{ id: 1, t: now + 1000 }], logs: [{ requests: [{ id: 2, t: now + 1000 }], playback: [{ id: 2, t: now - 1000 }] },
    { requests: [{ id: 1, t: now - 1000 }], playback: [] }], trail: [] };
  assert.deepEqual(observationsBefore(data, now), [{ id: 1, t: now - 1000 }, { id: 2, t: now - 1000 }]);
});

test('source windows retain the existing anchored 30-second and cross-source 60-second rules', () => {
  const data = { vrcx: [0, 29000, 31000, 59000].map(x => ({ id: 1, t: now + x })),
    logs: [{ requests: [{ id: 1, t: now + 90000 }], playback: [] }], trail: [{ id: 1, t: now + 200000 }] };
  assert.deepEqual(observationsBefore(data, now + DAY).map(e => e.t - now), [0, 31000, 200000]);
});

test('the production generator rejects identity-free research events', () => {
  assert.throws(() => buildPrior([{ id: 1, t: now }], 14), /requester attribution/);
  assert.throws(() => buildPrior({ schemaVersion: 1, capturedAt: now, vrcx: [{ id: 1, t: now }], trail: [], logs: [], attribution: [] }, 14), /requester attribution/);
});

test('published priors preserve equal budgets and contain aggregate data only', () => {
  const vrcx = [5, 500].flatMap((n, i) => Array.from({ length: n }, (_, j) => ({ id: i + 1, t: now - j % 5 * DAY - Math.floor(j / 5) * 60001 })));
  const data = { schemaVersion: 1, capturedAt: now, vrcx, trail: [], logs: [],
    attribution: vrcx.map(e => ({ ...e, uid: 'private-person-' + e.id, name: 'private-name-' + e.id, priority: 1, random: false })) };
  const prior = buildPrior(data, 14);
  assert.equal(prior.model, 'equalRequester');
  assert.equal(prior.activeDaysForFullWeight, 5);
  assert.equal(prior.referenceDate, '2026-10-01');
  assert.ok(!('referenceTime' in prior));
  prior.songs.forEach(s => close(s.score, 0.5));
  assert.ok(!JSON.stringify(prior).includes('private-'));
});
