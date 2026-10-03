// Input: private, already deduplicated research events [{id, t}] (Unix ms).
// Output contains only aggregate song scores, never request records or identities.
import { readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';

const [input, output] = process.argv.slice(2);
if (!input || !output) throw new Error('Usage: node scripts/build-initial-priority.mjs EVENTS.json OUTPUT.json');
const bytes = readFileSync(input);
const events = JSON.parse(bytes);
if (!Array.isArray(events) || !events.length) throw new Error('Expected nonempty event array');
let reference = 0;
for (const { id, t } of events) {
  if (!Number.isSafeInteger(id) || id <= 0 || !Number.isSafeInteger(t) || t <= 0) throw new Error('Invalid event');
  reference = Math.max(reference, t);
}
const scores = new Map();
for (const { id, t } of events) {
  scores.set(id, (scores.get(id) ?? 0) + 2 ** (-(reference - t) / (60 * 86400000)));
}
const max = Math.max(...scores.values());
const songs = [...scores].map(([songId, score]) => ({ songId, score: score / max * 0.5 }))
  .sort((a, b) => b.score - a.score || b.songId - a.songId);
writeFileSync(output, JSON.stringify({
  schemaVersion: 1,
  model: 'exponential',
  halfLifeDays: 60,
  referenceTime: new Date(reference).toISOString(),
  eventCount: events.length,
  eventSha256: createHash('sha256').update(bytes).digest('hex'),
  normalizationMax: 0.5,
  songs,
}, null, 2) + '\n');
console.log(`Generated ${songs.length} song priors from ${events.length} events.`);
