// Input: the private source snapshot from experiments/equal-priority/extract.mjs.
// Source attribution is required: an identity-free event array cannot establish
// equal requester budgets and must never silently regenerate a frequency prior.
import { readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { ACTIVE_DAYS, UNKNOWN_WEIGHT, observationsBefore, attributePeople, fairScores, normalizedPrior } from './initial-priority.mjs';

export function buildPrior(data, halfLifeDays) {
  if (data?.schemaVersion !== 1 || !Number.isSafeInteger(data.capturedAt) ||
      !['vrcx', 'trail', 'logs', 'attribution'].every(key => Array.isArray(data[key])) || !data.attribution.length) {
    throw Error('Initial priority requires a source snapshot with requester attribution');
  }
  const events = observationsBefore(data, data.capturedAt + 1);
  const attributed = attributePeople(events, data.attribution, data.capturedAt + 1);
  const { ranking, summary } = fairScores(attributed.events, halfLifeDays);
  if (!summary.identifiedPeople) throw Error('Initial priority requires identifiable requesters');
  return {
    schemaVersion: 1,
    model: 'equalRequester',
    halfLifeDays,
    activeDaysForFullWeight: ACTIVE_DAYS,
    unknownPoolWeight: UNKNOWN_WEIGHT,
    // Public provenance keeps only the observation date in Asia/Shanghai.
    referenceDate: new Date(events.at(-1).t + 8 * 3600000).toISOString().slice(0, 10),
    eventCount: events.length,
    normalizationMax: 0.5,
    normalization: '0.5 * log1p(songWeight) / log1p(maxSongWeight)',
    requesterSummary: { ...summary, conflicts: attributed.conflicts },
    songs: normalizedPrior(ranking),
  };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [input, output, halfLife] = process.argv.slice(2);
  if (!input || !output || !halfLife) throw Error('Usage: node scripts/build-initial-priority.mjs SOURCES.local.json OUTPUT.json HALF_LIFE_DAYS|none');
  const bytes = readFileSync(input);
  const prior = buildPrior(JSON.parse(bytes), halfLife === 'none' ? null : Number(halfLife));
  prior.sourceSha256 = createHash('sha256').update(bytes).digest('hex');
  writeFileSync(output, JSON.stringify(prior, null, 2) + '\n');
  console.log(`Generated ${prior.songs.length} equal-requester priors from ${prior.eventCount} events.`);
}
