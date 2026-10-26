// Read-only source extraction. The output is PRIVATE and must stay in artifacts/.
// Identities are replaced with snapshot-local opaque tokens, never printed.
import { DatabaseSync } from 'node:sqlite';
import { readFileSync, readdirSync, writeFileSync, mkdirSync } from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { observationsBefore } from '../../scripts/initial-priority.mjs';

const [vrcxPath, trailPath, logsPath, output] = process.argv.slice(2);
if (!output || !path.resolve(output).startsWith(path.resolve('artifacts') + path.sep)) throw Error('Usage: node experiments/equal-priority/extract.mjs VRCX.sqlite3 dancing_log.sqlite3 source-vrc-logs artifacts/.../sources.local.json');
const capturedAt = Date.now(), data = { schemaVersion: 1, capturedAt, vrcx: [], trail: [], logs: [], attribution: [] };
const names = new Map(), users = new Map();
const token = (map, value, prefix) => {
  if (!value) return null;
  if (!map.has(value)) map.set(value, prefix + (map.size + 1));
  return map.get(value);
};
function parseID(value) {
  try {
    const u = new URL(value.replace(/[,)}\]]+$/, ''));
    if (!['api.udon.dance', 'api.wannadance.online'].includes(u.hostname)) return null;
    const raw = u.pathname.toLowerCase() === '/api/songs/play' ? u.searchParams.get('id') : /^\/v\/(\d+)$/.exec(u.pathname.toLowerCase())?.[1];
    const id = /^\d+$/.test(raw || '') ? Number(raw) : NaN;
    return Number.isSafeInteger(id) && id > 0 ? id : null;
  } catch { return null; }
}
const valid = (id, t) => Number.isSafeInteger(id) && id > 0 && Number.isSafeInteger(t) && t > 0 && t <= capturedAt;
function attribute(id, t, name, uid, priority, random = false) {
  name = typeof name === 'string' ? name.trim().toLowerCase() : '';
  if (['random', 'unknown', 'system'].includes(name)) name = '';
  uid = typeof uid === 'string' && /^usr_[0-9a-f-]+$/i.test(uid) ? uid : '';
  data.attribution.push({ id, t, name: token(names, name, 'n'), uid: token(users, uid, 'u'), priority, random });
}
function query(file, sql) {
  const db = new DatabaseSync(file, { readOnly: true });
  try { db.exec('PRAGMA query_only=ON; PRAGMA busy_timeout=5000'); return db.prepare(sql).all(); }
  finally { db.close(); }
}
for (const r of query(vrcxPath, 'SELECT created_at, video_url, display_name, user_id FROM gamelog_video_play ORDER BY created_at, video_url')) {
  const id = parseID(r.video_url), t = Date.parse(r.created_at);
  if (!valid(id, t)) continue;
  data.vrcx.push({ id, t }); attribute(id, t, r.display_name, r.user_id, 1);
}
for (const file of readdirSync(logsPath).filter(f => f.endsWith('.txt')).sort()) {
  const log = { requests: [], playback: [] };
  for (const line of readFileSync(path.join(logsPath, file), 'utf8').split(/\r?\n/)) {
    const stamp = /^(\d{4})\.(\d{2})\.(\d{2}) (\d{2}:\d{2}:\d{2})/.exec(line);
    const t = stamp ? Date.parse(`${stamp[1]}-${stamp[2]}-${stamp[3]}T${stamp[4]}+08:00`) : NaN;
    if (!Number.isSafeInteger(t) || t <= 0 || t > capturedAt) continue;
    if (line.includes('DeserializeVideoUserData: userData = {')) {
      try {
        const payload = JSON.parse(line.slice(line.indexOf('userData = ') + 11).replace(/<\/color>\s*$/, ''));
        const id = parseID(payload.videoUrl);
        if (valid(id, t)) attribute(id, t, payload.playerName, null, 2, !!payload.isRandom);
      } catch { /* Ignore non-JSON diagnostics. */ }
    }
    const request = /Started video load for URL:|\[VRCX\] VideoPlay|Attempting to resolve URL/.test(line);
    const playback = /OnVideoStart: Started video:|Update: Started video:|PlayVideoInternal: Playing video/.test(line);
    if (!request && !playback) continue;
    const ids = new Set([...line.matchAll(/https?:\/\/[^\s"'<>]+/g)].map(m => parseID(m[0])).filter(id => valid(id, t)));
    for (const id of ids) (request ? log.requests : log.playback).push({ id, t });
  }
  data.logs.push(log);
}
for (const r of query(trailPath, "SELECT dance_external_id, played_at, requester_display_name, requester_user_id, request_type FROM playback_records WHERE dance_system_key='wannadance' ORDER BY played_at, dance_external_id")) {
  const id = Number(r.dance_external_id), t = Date.parse(r.played_at);
  if (!valid(id, t)) continue;
  data.trail.push({ id, t }); attribute(id, t, r.requester_display_name, r.requester_user_id, 3, r.request_type !== 'player');
}
mkdirSync(path.dirname(output), { recursive: true });
writeFileSync(output, JSON.stringify(data) + '\n');
const events = observationsBefore(data, capturedAt + 1);
console.log(JSON.stringify({ events: events.length, songs: new Set(events.map(e => e.id)).size,
  first: new Date(events[0].t).toISOString(), last: new Date(events.at(-1).t).toISOString(),
  vrcx: data.vrcx.length, trail: data.trail.length, logs: data.logs.length, attributionSignals: data.attribution.length,
  eventSha256: createHash('sha256').update(JSON.stringify(events)).digest('hex') }, null, 2));
