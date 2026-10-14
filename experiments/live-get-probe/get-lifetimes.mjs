// Offline LIVE-20261014-02 analysis. Reads only previously allowlisted traces.
// Usage: node get-lifetimes.mjs <private-lab-root> <public-output.json>
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import assert from 'node:assert/strict';

const root = path.resolve(process.argv[2]);
const output = path.resolve(process.argv[3]);
const read = file => JSON.parse(fs.readFileSync(file, 'utf8').replace(/^\uFEFF/, ''));
const ms = seconds => Math.round(seconds * 1000);
const sha = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const trace = id => read(path.join(root, 'sanitized-full', id + '.json'));

// Bound cold observation by publication, game Stop/End, or the last feedback
// confirming an ongoing operation. A stop acknowledgement is not its timestamp.
const specs = [
  ['fr-limited', 6776, 'c156', 281.085, 'first game StopVideo', [1, 2], [149.020, 165.257]],
  ['fr-unlimited', 6776, 'c156', 275.182, 'first game StopVideo', [1, 2], [205.476, 206.927]],
  ['fn-limited', 6332, '7643', 166.986, 'first game StopVideo', [1, 2], [124.646, 156.367]],
  ['fn-unlimited-retry1', 3194, '4b48', 158.260, 'target game StopVideo', [3, 4], [75.691, 77.032]],
  ['preview-limited-retry1', 3214, 'c123', 205.538, 'last seek feedback; exact preview stop unavailable', [2, 3], [96.935, 107.779]],
  ['preview-unlimited-retry1', 5396, 'a231', 131.797, 'seek feedback', [2, 3], [41.680, 46.020]],
  ['dual-limited', 10938, 'ca3a', 325.569, 'first target OnVideoEnd', [1, 2], [130.561, 325.569]],
  ['dual-unlimited', 1314, '3590', 200.723, 'dual playback feedback', [4, 5], [163.621, 177.578]],
];

function measure(requests, from, to) {
  const intervals = requests.map(r => ({id: r.id, start: Math.max(ms(r.start), from), end: Math.min(ms(r.end), to)}))
    .filter(r => r.end > r.start).sort((a, b) => a.start - b.start || a.id - b.id);
  const union = [];
  for (const r of intervals) {
    const last = union.at(-1);
    if (!last || r.start > last.end) union.push({...r, lastID: r.id});
    else if (r.end > last.end) Object.assign(last, {end: r.end, lastID: r.id});
  }
  const gaps = union.slice(1).map((r, i) => ({from: union[i].end / 1000, to: r.start / 1000,
    durationMs: r.start - union[i].end, previousRequestID: union[i].lastID, nextRequestID: r.id}));
  let count = 0, maximumConcurrent = 0, overlapMs = 0, previous = from;
  for (const [t, change] of intervals.flatMap(r => [[r.start, 1], [r.end, -1]]).sort((a, b) => a[0] - b[0] || a[1] - b[1])) {
    if (count >= 2) overlapMs += t - previous;
    count += change;
    maximumConcurrent = Math.max(maximumConcurrent, count);
    previous = t;
  }
  const coveredMs = union.reduce((n, r) => n + r.end - r.start, 0);
  assert.equal(count, 0);
  assert(coveredMs <= to - from && overlapMs <= coveredMs);
  return {from: from / 1000, to: to / 1000, maximumConcurrent, overlapMs, coveredMs,
    uncoveredMs: to - from - coveredMs, maximumGapMs: Math.max(0, ...gaps.map(g => g.durationMs)), gaps};
}

const stages = specs.map(([id, song, prefix, bound, boundReason, handoff, interaction]) => {
  const s = trace(id);
  assert.equal(s.batch, 'LIVE-20261014-02');
  const requests = s.requests.filter(r => r.key?.startsWith(prefix) && r.start !== undefined);
  const key = requests[0].key;
  assert(requests.every(r => r.key === key && Number.isFinite(r.end)));
  const events = s.events.filter(e => e.resource_key === key || e.key === key);
  for (const r of requests) assert.equal(events.find(e => e.event === 'request_started' && e.request_id === r.id).method, 'GET');
  const publication = events.find(e => e.event === 'download_published')?.t;
  const until = Math.min(bound, publication ?? Infinity);
  const observation = measure(requests, ms(requests[0].start), ms(until));
  for (const g of observation.gaps) g.kind = g.previousRequestID === handoff[0] && g.nextRequestID === handoff[1]
    ? 'initial resolver/WMF handoff' : 'subsequent all-GET gap';
  const traceBytes = fs.readFileSync(path.join(root, 'sanitized-full', id + '.json'), 'utf8').replace(/\r\n/g, '\n');
  return {id, song, key, traceSHA256LF: sha(traceBytes), publication: publication ?? null,
    bound, boundReason, observation, interaction: measure(requests, ms(interaction[0]), ms(Math.min(interaction[1], until))),
    WMFOnly: measure(requests.filter(r => r.role === 'WMF-player'), ms(requests[0].start), ms(until)),
    flightIDs: [...new Set(events.filter(e => e.event === 'cache_task_attached').map(e => e.flight_id))],
    requests: requests.map(({id, role, range, start, end, status, cache, outcome}) => ({id, role, range, start, end, status, cache, outcome}))};
});

// These are deliberately NOT evidence for protecting an actively playing,
// unfinished download throughout long GET-free periods.
const exclusions = [
  {stage: 'preview-limited-retry1', from: 205.538, to: 275.602,
    reason: 'After last seek feedback and before stop acknowledgement; no exact preview stop timestamp. Two later 517 ms gaps are excluded from confirmed-operation maximum.'},
  {stage: 'preview-unlimited', from: 6.917, to: 34.433, publication: 24.275,
    reason: 'Old preview was stopped; residual request then preparation. Download continues without GET, finishes before new user playback. Not a 27.516 second active-playback gap.'},
  {stage: 'fn-unlimited-retry1', song: 3136, from: 20.135, to: 50.413,
    reason: 'Previous video stopped during preparation; its no-reader download tail is not the target 3194 playback.'},
  {stage: 'fr-hot', from: 38.869, to: 46.481,
    reason: 'Complete SWD cache; refresh WMF returns 304. No unfinished download to protect.'},
  {stage: 'dual-hot', from: 24.608, to: 35.093,
    reason: 'Complete SWD cache; formal WMF returns 304 and uses client cache. Two playing UIs do not imply two persistent GETs.'},
];
const allTraces = fs.readdirSync(path.join(root, 'sanitized-full')).filter(n => n.endsWith('.json')).map(n => trace(n.slice(0, -5)));
const preemptions = allTraces.flatMap(s => s.events.filter(e => e.event === 'cache_task_preempted'));
const gaps = stages.flatMap(s => s.observation.gaps);
const result = {schemaVersion: 1, batch: 'LIVE-20261014-02', sourceSHA: '3fae4b81642f27a7a406a828bd8fa0eb79dc8e94',
  method: 'Per-resource union of validated downstream GET lifetimes, including resolver GETs. Clip at publication/Stop/End/last confirmed action. Integer milliseconds, half-open intervals. Overlap duration means at least two GETs, not sum of request-pair overlaps.',
  limitations: ['HTTP start/finish logs approximate demand lifetime; cancellation invalidates demand before finish logging, and registration follows start logging. No lock-level P0 timestamps.',
    'Game callbacks have one-second resolution; user acknowledgements do not timestamp the actual action.',
    'Queue prefetch disabled; no actual competing-task preemption tested. Grace necessity, priority semantics and duration remain undecided; a one-second candidate is not an accepted or validated policy.'],
  aggregate: {coldWindows: stages.length, gapCount: gaps.length, maximumGapMs: Math.max(...gaps.map(g => g.durationMs)),
    totalGapMs: gaps.reduce((n, g) => n + g.durationMs, 0), preemptionEventsAcrossAll14Stages: preemptions.length},
  stages, exclusions};
assert.equal(result.aggregate.maximumGapMs, 514);
assert.equal(result.aggregate.gapCount, 26);
assert.equal(preemptions.length, 0);
for (const s of stages) {
  assert.equal(s.flightIDs.length, 1);
  assert.equal(s.observation.uncoveredMs, s.observation.gaps.reduce((n, g) => n + g.durationMs, 0));
  if (s.id.startsWith('dual-')) assert.equal(s.interaction.uncoveredMs, 0);
}
const text = JSON.stringify(result, null, 2) + '\n';
assert(!/Himalia|C:\\Users\\|https?:\/\/|usr_[a-f0-9]|wrld_[a-f0-9]|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/i.test(text));
fs.writeFileSync(output, text);
console.log(JSON.stringify(result.aggregate));
