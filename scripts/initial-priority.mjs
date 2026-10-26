// Offline initial priors only. Runtime local demand keeps its own 60-day decay.
export const DAY = 86400000;
export const ACTIVE_DAYS = 5;
export const UNKNOWN_WEIGHT = 0.25;

const chronological = (a, b) => a.t - b.t || a.id - b.id;
const before = (rows, cut) => rows.filter(e => e.t < cut).sort(chronological);
const nearby = (times, t, window) => times?.some(x => Math.abs(x - t) <= window);

// Filter sources BEFORE cross-source deduplication, fallback and attribution:
// future diagnostics must not change what was knowable at a backtest cutoff.
export function observationsBefore(data, cut) {
  const vrcx = before(data.vrcx, cut), accepted = [], last = new Map(), byID = new Map();
  const remember = e => {
    if (!byID.has(e.id)) byID.set(e.id, []);
    byID.get(e.id).push(e.t);
  };
  for (const e of vrcx) {
    if (!last.has(e.id) || e.t - last.get(e.id) > 30000) {
      accepted.push(e); last.set(e.id, e.t);
    }
    remember(e);
  }
  const raw = [];
  for (const log of data.logs) {
    const requests = before(log.requests, cut);
    raw.push(...requests);
    raw.push(...before(log.playback, cut).filter(e => !requests.some(r => r.id === e.id && Math.abs(r.t - e.t) <= 600000)));
  }
  raw.sort(chronological);
  const rawLast = new Map();
  for (const e of raw) {
    if (rawLast.has(e.id) && e.t - rawLast.get(e.id) <= 30000) continue;
    rawLast.set(e.id, e.t);
    if (nearby(byID.get(e.id), e.t, 60000)) continue;
    accepted.push(e); remember(e);
  }
  for (const e of before(data.trail, cut)) {
    if (nearby(byID.get(e.id), e.t, 60000)) continue;
    accepted.push(e); remember(e);
  }
  return accepted.sort(chronological);
}

export function attributePeople(events, observations, cut) {
  const available = observations.filter(a => a.t < cut), aliases = new Map(), bySong = new Map();
  for (const a of available) {
    if (a.name && a.uid) {
      if (!aliases.has(a.name)) aliases.set(a.name, new Set());
      aliases.get(a.name).add(a.uid);
    }
    if (!bySong.has(a.id)) bySong.set(a.id, []);
    bySong.get(a.id).push(a);
  }
  const identity = a => {
    if (a.random) return null;
    if (a.uid) return 'id:' + a.uid;
    if (!a.name) return null;
    const ids = aliases.get(a.name);
    return ids?.size === 1 ? 'id:' + [...ids][0] : ids?.size > 1 ? null : 'name:' + a.name;
  };
  let conflicts = 0;
  const attributed = events.filter(e => e.t < cut).map(e => {
    const matches = (bySong.get(e.id) || []).filter(a => Math.abs(a.t - e.t) <= 60000);
    const level = Math.max(0, ...matches.map(a => a.priority));
    const people = new Set(matches.filter(a => a.priority === level).map(identity));
    if (people.size > 1) conflicts++;
    return { ...e, person: people.size === 1 ? [...people][0] : null };
  });
  return { events: attributed, conflicts };
}

export function fairScores(events, halfLifeDays) {
  if (halfLifeDays !== null && (!Number.isFinite(halfLifeDays) || halfLifeDays <= 0)) throw Error('Expected a positive half-life or null for no decay');
  const people = new Map(), unknown = [], scores = new Map();
  for (const e of events) {
    if (!Number.isSafeInteger(e.id) || e.id <= 0 || !Number.isSafeInteger(e.t) || e.t <= 0 || !(e.person === null || typeof e.person === 'string' && e.person.length)) throw Error('Invalid attributed event');
    scores.set(e.id, 0);
    if (e.person === null) { unknown.push(e); continue; }
    if (!people.has(e.person)) people.set(e.person, []);
    people.get(e.person).push(e);
  }
  const distribute = (rows, budget) => {
    // A common reference factor cancels within a person. Their latest event
    // avoids all-zero weights for inactive people under short half-lives.
    const reference = Math.max(...rows.map(e => e.t));
    const weights = rows.map(e => halfLifeDays === null ? 1 : 2 ** (-(reference - e.t) / DAY / halfLifeDays));
    const total = weights.reduce((a, b) => a + b, 0);
    rows.forEach((e, i) => scores.set(e.id, scores.get(e.id) + budget * weights[i] / total));
  };
  let fullWeightPeople = 0, totalPersonWeight = 0;
  for (const rows of people.values()) {
    const days = new Set(rows.map(e => Math.floor((e.t + 8 * 3600000) / DAY))).size;
    const budget = Math.min(1, days / ACTIVE_DAYS);
    fullWeightPeople += Number(days >= ACTIVE_DAYS);
    totalPersonWeight += budget;
    distribute(rows, budget);
  }
  if (unknown.length) distribute(unknown, UNKNOWN_WEIGHT);
  return {
    ranking: [...scores].map(([id, score]) => ({ id, score })).sort((a, b) => b.score - a.score || b.id - a.id),
    summary: { identifiedPeople: people.size, fullWeightPeople, partialWeightPeople: people.size - fullWeightPeople,
      identifiedEvents: events.length - unknown.length, unknownEvents: unknown.length, totalPersonWeight,
      unknownPoolWeight: unknown.length ? UNKNOWN_WEIGHT : 0 },
  };
}

export function normalizedPrior(ranking) {
  const maximum = ranking[0]?.score;
  if (!(maximum > 0)) throw Error('Cannot generate an empty or zero-weight prior');
  return ranking.map(({ id, score }) => ({ songId: id, score: 0.5 * Math.log1p(score) / Math.log1p(maximum) }));
}
