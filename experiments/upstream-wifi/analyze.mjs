import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

// Offline only: verify the frozen sanitized export, never contact the upstream.
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const batch = 'wifi-ip-selection-20261029';
const evidence = path.join(root, 'docs/evidence', batch);
const args = process.argv.slice(2);
assert(args.length <= 1, 'Usage: node experiments/upstream-wifi/analyze.mjs [extracted-evidence-directory]');
const input = args[0] ? path.resolve(args[0]) : path.join(root, 'artifacts/evidence', batch, 'docs/evidence', batch);
const json = file => JSON.parse(fs.readFileSync(file, 'utf8').replace(/^\uFEFF/, ''));
const lines = file => fs.readFileSync(file, 'utf8').trim().split(/\r?\n/).map(line => JSON.parse(line));
const median = values => {
  if (!values.length) return null;
  const sorted = [...values].sort((a, b) => a - b), middle = Math.floor(sorted.length / 2);
  return sorted.length % 2 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2;
};
const near = (a, b) => assert(Math.abs(a - b) < 1e-8 * Math.max(1, Math.abs(b)), `${a} != ${b}`);
const rows = lines(path.join(input, 'single-lane-trials.jsonl'));
const events = lines(path.join(input, 'events.jsonl'));
const plan = json(path.join(evidence, 'plan.json'));
const selection = json(path.join(evidence, 'selection.json'));
const summary = json(path.join(evidence, 'summary.json'));
const speed = row => row.ok ? row.bytes / 1048576 / (row.elapsedMs / 1000) : null;
const completed = rows.filter(row => !row.interrupted);
const group = (phase, protocol, ip) => completed.filter(r => r.phase === phase && r.protocol === protocol && r.ip === ip);

assert.equal(rows.length, summary.records);
assert.equal(completed.length, summary.logicalRecords);
assert.equal(rows.length - completed.length, summary.interrupted);
assert.equal(new Set(completed.map(r => `${r.phase}/${r.protocol}/${r.ip}/${r.round}`)).size, completed.length);
assert.equal(rows.reduce((sum, r) => sum + r.bytes, 0), summary.readBytes);
assert(summary.readBytes <= plan.bodyCap);
assert.equal(new Set(plan.candidates).size, plan.candidateCount);
assert.equal(events.filter(e => ['start', 'resumed', 'stopped'].includes(e.kind)).at(-1).kind, 'stopped');
assert(events.some(e => e.kind === 'measurement-complete'));
const phaseCounts = {};
for (const row of rows) {
  const count = phaseCounts[`${row.phase}/${row.protocol}`] ??= { records: 0, passed: 0, failed: 0, interrupted: 0, bytes: 0 };
  count.records++;
  count[row.interrupted ? 'interrupted' : row.ok ? 'passed' : 'failed']++;
  count.bytes += row.bytes;
  assert.equal(row.concurrency, 1);
  assert.equal(row.sample.offset, plan.offset);
  assert.equal(row.sample.requestedBytes, plan.bytes);
  assert.equal(row.sample.targetIP, row.ip);
  assert(row.activeTrialsAtStart <= plan.screenWorkers);
  if (!['wide-screen', 'dns-control'].includes(row.phase)) assert.equal(row.activeTrialsAtStart, 1);
  if (row.ok) {
    assert.equal(row.bytes, plan.bytes);
    assert.equal(row.sample.receivedBytes, plan.bytes);
    assert.equal(row.sample.status, 206);
    assert.equal(row.sample.failure, null);
    assert.equal(row.sample.connection, 1);
    assert.equal(row.sample.reused, false);
    assert.deepEqual(row.sample.hashes, [plan.sha256]);
    near(speed(row), row.mibPerSec);
  } else assert.equal(row.mibPerSec, null);
}
assert.deepEqual(phaseCounts, summary.samples);
const intervals = rows.map(r => ({ ip: r.ip, start: r.startSeconds, end: r.startSeconds + r.elapsedMs / 1000 }));
const points = intervals.flatMap(r => [[r.start, 1], [r.end, -1]]).sort((a, b) => a[0] - b[0] || a[1] - b[1]);
let active = 0, peak = 0;
for (const [, delta] of points) { active += delta; peak = Math.max(peak, active); }
assert.equal(peak, summary.integrity.observedPeakConcurrentRequests);
assert(peak <= plan.screenWorkers);
for (const ip of plan.candidates) {
  const list = intervals.filter(r => r.ip === ip).sort((a, b) => a.start - b.start);
  for (let i = 1; i < list.length; i++) assert(list[i].start >= list[i - 1].end - 0.0001, `Overlapping IP: ${ip}`);
}
for (const protocol of plan.protocols) {
  const screen = completed.filter(r => r.phase === 'wide-screen' && r.protocol === protocol);
  assert.deepEqual(screen.map(r => r.ip).sort(), [...plan.candidates].sort());
  const choice = selection.choices[protocol];
  const confirmedIPs = [...new Set(completed.filter(r => r.phase === 'confirm' && r.protocol === protocol).map(r => r.ip))];
  for (const ip of confirmedIPs) assert.deepEqual(group('confirm', protocol, ip).map(r => r.round).sort(), [1, 2, 3]);
  const eligible = confirmedIPs.filter(ip => group('confirm', protocol, ip).every(r => r.ok));
  eligible.sort((a, b) => median(group('confirm', protocol, a).map(r => r.elapsedMs)) - median(group('confirm', protocol, b).map(r => r.elapsedMs)));
  assert.deepEqual(choice.ranked, eligible);
  assert.deepEqual(choice.finalists, [...plan.dns, ...eligible.filter(ip => !plan.dns.includes(ip)).slice(0, 2)]);
  for (const ip of choice.finalists) {
    const samples = group('holdout', protocol, ip);
    assert.deepEqual(samples.map(r => r.round).sort(), [1, 2, 3, 4, 5, 6]);
    assert(samples.every(r => r.startSeconds > selection.tSeconds));
    const expected = summary.finalRows.find(r => r.protocol === protocol && r.ip === ip);
    assert(expected);
    assert.equal(samples.filter(r => r.ok).length, expected.passed);
    const value = median(samples.filter(r => r.ok).map(speed));
    if (value === null) assert.equal(expected.medianMiBPerSec, null);
    else near(value, expected.medianMiBPerSec);
  }
}
for (const expected of summary.pairedComparisons) {
  const candidates = group('holdout', expected.protocol, expected.candidate);
  const baselines = group('holdout', expected.protocol, expected.baseline);
  const pairs = candidates.flatMap(c => {
    const b = baselines.find(r => r.round === c.round);
    return c.ok && b?.ok ? [{ round: c.round, ratio: speed(c) / speed(b) }] : [];
  });
  assert.equal(pairs.length, expected.pairs.length);
  pairs.forEach((p, i) => { assert.equal(p.round, expected.pairs[i].round); near(p.ratio, expected.pairs[i].ratio); });
  const value = median(pairs.map(p => p.ratio));
  if (value === null) assert.equal(expected.medianRatio, null); else near(value, expected.medianRatio);
  const wins = pairs.filter(p => p.ratio > 1).length;
  const candidateFailures = candidates.filter(r => !r.ok).length, baselineFailures = baselines.filter(r => !r.ok).length;
  assert.equal(wins, expected.positivePairs);
  assert.equal(candidateFailures, expected.candidateFailures);
  assert.equal(baselineFailures, expected.baselineFailures);
  assert.equal(expected.engineeringGainSupported, pairs.length >= 5 && wins >= 5 && value >= 1.25 && candidateFailures <= baselineFailures);
}
const sharedIPs = selection.choices.http.finalists.filter(ip => selection.choices.https.finalists.includes(ip));
const comparisons = sharedIPs.map(ip => {
  const http = group('holdout', 'http', ip), https = group('holdout', 'https', ip);
  const pairs = http.map(h => {
    const s = https.find(r => r.round === h.round);
    return { round: h.round, httpOK: h.ok, httpsOK: s.ok, httpElapsedMs: h.elapsedMs, httpsElapsedMs: s.elapsedMs, httpMiBPerSec: speed(h), httpsMiBPerSec: speed(s), ratio: h.ok && s.ok ? speed(h) / speed(s) : null };
  });
  const successful = pairs.filter(p => p.ratio !== null);
  return { ip, dns: plan.dns.includes(ip), httpPassed: http.filter(r => r.ok).length, httpsPassed: https.filter(r => r.ok).length, successfulPairs: successful.length, httpFasterPairs: successful.filter(p => p.ratio > 1).length, medianRatio: median(successful.map(p => p.ratio)), pairs };
});
const protocol = { schemaVersion: 1, phase: 'holdout', pairing: 'Same IP, same block, same round; protocol order alternated by round, never concurrent.', limitation: 'Protocol, port and path effects are not isolated TLS cost; no throughput ratio when either request is incomplete.', comparisons };
const savedProtocol = path.join(evidence, 'protocol-comparison.json');
if (fs.existsSync(savedProtocol)) assert.deepEqual(protocol, json(savedProtocol));
console.log(JSON.stringify({ verified: true, records: rows.length, logicalRecords: completed.length, peakConcurrentRequests: peak, privateBindingEvidence: 'Checked at collection and closeout; intentionally absent from this public recheck.', protocol }, null, 2));
