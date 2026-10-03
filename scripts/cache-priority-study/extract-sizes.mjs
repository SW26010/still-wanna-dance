// Read-only size snapshot; publishes no requests, identities, titles, or local paths.
import { DatabaseSync } from 'node:sqlite';
import { lstatSync, mkdirSync, writeFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { createHash } from 'node:crypto';
const [storageRoot, outputPath, explicitDate] = process.argv.slice(2);
if (!outputPath) throw Error('Usage: node extract-sizes.mjs STORAGE_ROOT OUTPUT_JSON');
const db = new DatabaseSync(join(storageRoot, 'stepstash.sqlite'), { readOnly: true });
const rows = db.prepare(`SELECT c.song_id, c.version_key, v.file_bytes
 FROM current_videos c JOIN video_versions v ON v.version_key=c.version_key
 ORDER BY c.song_id`).all();
const keys = [...new Set(rows.map(r => r.version_key))].sort();
const resources = [], keyToID = new Map();
let physicallyPresent = 0, mismatches = 0, missing = 0;
for (const key of keys) {
  const declared = Number(rows.find(r => r.version_key === key).file_bytes);
  const id = `r${String(resources.length + 1).padStart(5, '0')}`;
  keyToID.set(key, id);
  let actual = null;
  try { const stat = lstatSync(join(storageRoot, 'videos', key + '.mp4')); if (stat.isFile() && !stat.isSymbolicLink()) actual = stat.size; } catch (e) { if (e.code !== 'ENOENT') throw e; }
  if (actual !== null) { physicallyPresent++; if (actual !== declared) mismatches++; } else missing++;
  const bytes = actual ?? declared;
  if (!Number.isSafeInteger(bytes) || bytes <= 0) throw Error('Invalid positive resource size');
  resources.push({ resource: id, bytes, physicalFilePresent: actual !== null, sizeSource: actual !== null ? 'physical-stat' : 'database-metadata' });
}
const songs = rows.map(r => ({ songId: Number(r.song_id), resource: keyToID.get(r.version_key) })).filter(r => Number.isSafeInteger(r.songId) && r.songId > 0).sort((a,b) => a.songId-b.songId);
db.close();
const payload = JSON.stringify({ songs, resources });
const snapshotDate = explicitDate ?? new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date());
const report = { schemaVersion: 1, snapshotDate, timezone: 'Asia/Shanghai',
 sizePolicy: 'Current song->resource mapping; actual regular-file length when present, database file_bytes otherwise; charge each shared resource once. Snapshot is contemporary, not reconstructed historical versions.',
 summary: { songs: songs.length, uniqueResources: resources.length, physicalFiles: physicallyPresent, metadataOnly: missing, lengthMismatches: mismatches, fullCatalogBytes: resources.reduce((s,r)=>s+r.bytes,0) },
 sha256: createHash('sha256').update(payload).digest('hex'), songs, resources };
mkdirSync(dirname(outputPath), { recursive: true });
writeFileSync(outputPath, JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify({ ...report, songs: undefined, resources: undefined }, null, 2));
