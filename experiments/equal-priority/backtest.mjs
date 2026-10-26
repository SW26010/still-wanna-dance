// Full-candidate AUC over spaced 30/45/60-day windows across ALL future data.
// Private observations stay local; summary.json contains aggregate results only.
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { DAY, observationsBefore, attributePeople, fairScores } from '../../scripts/initial-priority.mjs';
import { sampleWindowStarts } from './windows.mjs';

const [input, output, samples = '5'] = process.argv.slice(2);
if (!output) throw Error('Usage: node experiments/equal-priority/backtest.mjs sources.local.json output-directory [window-count=5]');
const windowCount = Number(samples);
const bytes = readFileSync(input), data = JSON.parse(bytes);
const stamp = s => Date.parse(s + 'T00:00:00+08:00');
const date = t => new Date(t + 8 * 3600000).toISOString().slice(0, 10);
const halves = [1, 3, 7, 14, 30, 60, 90, 120, 180, 365, null];
const horizons = [30, 45, 60], boundary = stamp('2026-04-01');
const label = h => h === null ? 'none' : String(h);
const mean = values => values.length ? values.reduce((a, b) => a + b, 0) / values.length : null;
const events = observationsBefore(data, data.capturedAt + 1);
const firstDay = Math.floor((events[0].t + 8 * 3600000) / DAY) * DAY - 8 * 3600000;
const completeEnd = Math.floor((events.at(-1).t + 8 * 3600000) / DAY) * DAY - 8 * 3600000;
const maxTrainingDays = 320, maxCut = Math.min(firstDay + maxTrainingDays * DAY, completeEnd - 60 * DAY);
const byDay = new Map();
for (const e of events) {
  if (e.t >= completeEnd) continue;
  const day = date(e.t);
  if (!byDay.has(day)) byDay.set(day, new Set());
  byDay.get(day).add(e.id);
}
const plans = [];
const cuts = [];
for (let cut = stamp('2025-09-01'); cut <= maxCut; cut += 7 * DAY) cuts.push(cut);
if (cuts.at(-1) !== maxCut) cuts.push(maxCut);
for (const cut of cuts) for (const horizon of horizons) {
  const starts = sampleWindowStarts(cut, completeEnd, horizon, windowCount);
  starts.forEach((start, windowIndex) => plans.push({ cut, start, horizon, windowIndex }));
}
const historyCache = new Map();
function historyAt(cut) {
  if (!historyCache.has(cut)) {
    const history = observationsBefore(data, cut), attributed = attributePeople(history, data.attribution, cut);
    const rankings = Object.fromEntries(halves.map(h => [label(h), fairScores(attributed.events, h).ranking]));
    const count = new Set(history.map(e => e.id)).size;
    if (Object.values(rankings).some(r => r.length !== count)) throw Error('Candidate domain differs across half-lives');
    historyCache.set(cut, { count, historyEvents: history.length,
      attribution: { ...fairScores(attributed.events, 30).summary, conflicts: attributed.conflicts }, rankings });
  }
  return historyCache.get(cut);
}
const maxCandidates = Math.max(...plans.map(p => historyAt(p.cut).count));
const curveN = [...new Set([0, ...Array.from({ length: Math.floor(maxCandidates / 25) }, (_, i) => (i + 1) * 25), maxCandidates])];
function evaluate(ranking, start, horizon) {
  const ranks = new Map(ranking.map((r, i) => [r.id, i + 1])), count = ranking.length, days = [], curves = [];
  for (let day = 0; day < horizon; day++) {
    const songs = byDay.get(date(start + day * DAY));
    if (!songs?.size) continue;
    const rr = [...songs].map(id => ranks.get(id) ?? Infinity);
    // Exact discrete trapezoidal integral C(0)..C(M): a hit at rank r
    // contributes M-r+0.5. New songs outside the historical universe score 0.
    const rawAUC = mean(rr.map(r => Math.max(0, count - r + 0.5)));
    days.push({ day: date(start + day * DAY), offset: day, songs: songs.size, rawAUC, normalizedAUC: rawAUC / count,
      ceiling: rr.filter(Number.isFinite).length / rr.length });
    curves.push(curveN.map(n => rr.filter(r => r <= n).length / rr.length));
  }
  const part = rows => Object.fromEntries(['rawAUC', 'normalizedAUC', 'ceiling'].map(k => [k, mean(rows.map(d => d[k]))]));
  return { activeDays: days.length, ...part(days), first: part(days.filter(d => d.offset < 7)),
    last: part(days.filter(d => d.offset >= horizon - 7)),
    curve: curveN.map((_, i) => mean(curves.map(c => c[i]))), days };
}
const folds = plans.map(({ cut, start, horizon, windowIndex }) => {
  const history = historyAt(cut);
  return { cut: date(cut), start: date(start), endExclusive: date(start + horizon * DAY), horizon, windowIndex,
    trainingDays: (cut - firstDay) / DAY, futureDays: (completeEnd - cut) / DAY, delayDays: (start - cut) / DAY,
    candidates: history.count,
    trainingEvents: history.historyEvents, attribution: history.attribution,
    models: Object.fromEntries(halves.map(h => [label(h), evaluate(history.rankings[label(h)], start, horizon)])) };
}).filter(f => f.models['30'].activeDays > 0);
const periods = ['all', 'earlier', 'later', 'longest'];
const inPeriod = (f, period) => period === 'all' || (period === 'earlier' ? stamp(f.cut) < boundary :
  period === 'later' ? stamp(f.cut) >= boundary : stamp(f.cut) === maxCut);
const groupsByCut = rows => [...Map.groupBy(rows, f => f.cut).values()];
// Windows first, then training cutoffs: more remaining days or more available
// test windows never give one training history a larger overall vote.
function summarize(period, sensitivity = 'cutEqual') {
  return Object.fromEntries(horizons.map(horizon => [horizon, Object.fromEntries(halves.map(h => {
    let windows = folds.filter(f => inPeriod(f, period) && f.horizon === horizon);
    if (sensitivity === 'halfDays') windows = windows.filter(f => f.models[label(h)].activeDays >= horizon / 2);
    const groups = groupsByCut(windows), models = windows.map(f => f.models[label(h)]);
    const metric = k => mean(groups.map(group => {
      const ms = group.map(f => f.models[label(h)]), rows = sensitivity === 'dayEqual' ? ms.flatMap(m => m.days) : ms;
      return mean(rows.map(r => r[k]));
    }));
    return [label(h), { trainingCuts: groups.length, windows: windows.length, observedDayOccurrences: models.reduce((n, m) => n + m.activeDays, 0),
      minCandidates: windows.length ? Math.min(...windows.map(f => f.candidates)) : null,
      maxCandidates: windows.length ? Math.max(...windows.map(f => f.candidates)) : null,
      ...Object.fromEntries(['rawAUC', 'normalizedAUC', 'ceiling'].map(k => [k, metric(k)])) }];
  }))]));
}
const summary = Object.fromEntries(periods.map(p => [p, summarize(p)]));
const overall = Object.fromEntries(periods.map(period => [period, Object.fromEntries(halves.map(h => [label(h),
  Object.fromEntries(['rawAUC', 'normalizedAUC'].map(k => [k, mean(horizons.map(d => summary[period][d][label(h)][k]))]))]))]));
const selected = [...halves].sort((a, b) => overall.all[label(b)].normalizedAUC - overall.all[label(a)].normalizedAUC)[0];
function meanCurves(period, horizon) {
  const groups = groupsByCut(folds.filter(f => inPeriod(f, period) && f.horizon === horizon));
  return Object.fromEntries(halves.map(h => [label(h), curveN.map((_, i) =>
    mean(groups.map(group => mean(group.map(f => f.models[label(h)].curve[i])))))]));
}
const curves = Object.fromEntries(periods.map(period => [period,
  Object.fromEntries(horizons.map(horizon => [horizon, meanCurves(period, horizon)]))]));
const differences = [];
for (const horizon of horizons) for (const baseline of [1, 7, 14, 30, 60]) {
  const groups = groupsByCut(folds.filter(f => f.horizon === horizon));
  const delta = groups.map(group => mean(group.map(f => f.models[label(selected)].normalizedAUC - f.models[label(baseline)].normalizedAUC)));
  differences.push({ horizon, baseline, trainingCuts: delta.length, mean: mean(delta), min: Math.min(...delta), max: Math.max(...delta),
    positiveCuts: delta.filter(d => d > 0).length });
}
const latest = attributePeople(events, data.attribution, data.capturedAt + 1);
const result = { schemaVersion: 3, candidates: halves, horizons, selectedHalfLifeDays: selected,
  selection: 'Full-candidate normalized AUC: observed days -> sampled future windows -> training cutoffs -> 30/45/60-day horizons, each level equal-weighted. No fixed Top-N cutoff.',
  sampling: { windowCount, maxTrainingDays, trainingStart: date(firstDay), firstCut: date(cuts[0]), lastCut: date(maxCut),
    trainingCuts: cuts.length, trainingCutStepDays: 7, includeExactMaxCut: true,
    startRule: 'round(i * (futureDays - horizon) / (windowCount - 1)) calendar days after the fixed training cutoff; i=0..windowCount-1, deduplicated',
    groupBoundary: date(boundary) },
  audit: { previousExperiment: 'The 7-day/Top-1000 pilot and adjacent-only 30/45/60-day evaluation were superseded; outputs remain in ignored short-horizon/ and adjacent-windows/.',
    candidateExtension: '1 and 3 days were added after the pilot’s 7-day lower boundary won. Data have been examined before; this is retrospective model comparison, not a blind holdout.',
    dependence: 'Training histories are nested. Test windows overlap within and across cutoffs and horizons. Earlier/later groups refer to training cutoffs, share future observations, and are not independent validation sets.' },
  dataset: { inputSha256: createHash('sha256').update(bytes).digest('hex'), events: events.length, songs: new Set(events.map(e => e.id)).size,
    first: new Date(events[0].t).toISOString(), last: new Date(events.at(-1).t).toISOString(), completeDaysBefore: date(completeEnd),
    attribution: { ...fairScores(latest.events, selected).summary, conflicts: latest.conflicts } },
  summary, overall, curves: { n: curveN, maxCandidates, periods: curves }, differences,
  sensitivity: Object.fromEntries(['halfDays', 'dayEqual'].map(mode => [mode, Object.fromEntries(periods.map(p => [p, summarize(p, mode)]))])),
  folds };
mkdirSync(output, { recursive: true });
writeFileSync(path.join(output, 'results.local.json'), JSON.stringify(result, null, 2) + '\n');
const { folds: _, ...publicSummary } = result;
// Exact observation times stay in results.local.json, not public provenance.
publicSummary.dataset = { ...result.dataset, first: date(events[0].t), last: date(events.at(-1).t) };
publicSummary.curves = { ...result.curves, periods: { all: curves.all, longest: curves.longest } };
publicSummary.trainingHistories = groupsByCut(folds).map(group => {
  const { cut, trainingDays, futureDays, candidates, trainingEvents } = group[0];
  return { cut, trainingDays, futureDays, candidates, trainingEvents, windows: horizons.map(horizon => {
    const windows = group.filter(f => f.horizon === horizon);
    return { horizon, starts: windows.map(f => f.start), activeDays: windows.map(f => f.models['30'].activeDays) };
  }) };
});
// Keep the public plotting/summary input compact; full precision and daily
// observations remain in the ignored local result, not in published evidence.
writeFileSync(path.join(output, 'summary.json'), JSON.stringify(publicSummary,
  (_, value) => typeof value === 'number' && !Number.isInteger(value) ? Number(value.toFixed(10)) : value) + '\n');
console.log(JSON.stringify({ dataset: result.dataset, selectedHalfLifeDays: selected, maxCandidates, overall, differences }, null, 2));

