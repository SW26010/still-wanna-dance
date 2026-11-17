// Summarize media-map.mjs output without publishing paths or resource keys.
// Usage: node summarize-media-map.mjs <scan-dir> <summary.json> [playback.json]
import fs from 'node:fs';
import path from 'node:path';

const [input, output, playback] = process.argv.slice(2);
if (!input || !output) throw new Error('usage: summarize-media-map.mjs <scan-dir> <summary.json>');
const rows = fs.readdirSync(input).filter(n => /^part-\d+\.jsonl$/.test(n)).sort()
  .flatMap(n => fs.readFileSync(path.join(input, n), 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse));
const valid = rows.filter(r => !r.error);
const matched = valid.filter(r => r.catalogSeconds > 0);
const B = 1048576;
const round = n => Math.round(n * 1e6) / 1e6;
function stats(values) {
  const a = values.filter(Number.isFinite).sort((a, b) => a - b);
  if (!a.length) return { count: 0 };
  return Object.fromEntries([['count', a.length], ['min', a[0]], ...[.01,.05,.5,.95,.99].map(q =>
    [`p${q * 100}`, a[Math.min(a.length - 1, Math.floor(q * a.length))]]), ['max', a.at(-1)]]
    .map(([k,v]) => [k, round(v)]));
}
function summarize(files) {
  const errors = [], containerErrors = [], spans = [], nextKey = [], decodeLead = [];
  const margins = [-10,-6,-3,-2,0,2,5,10].map(seconds => ({ seconds, early: 0, earlyOver5: 0, lateOver5: 0, lateOver15: 0, fileEarly: [] }));
  for (const r of files) {
    let count = 0;
    const early = margins.map(() => 0);
    for (let b = 1; b < r.blocks.length; b++) {
      const p = r.blocks[b];
      if (p[0] === null) continue;
      count++;
      const linear = r.catalogSeconds * b * B / r.bytes, error = linear - p[0];
      errors.push(error); containerErrors.push(r.containerSeconds * b * B / r.bytes - p[0]);
      spans.push(p[2] - p[0]); decodeLead.push(p[0] - p[1]);
      if (p[7] !== null) nextKey.push(p[7] - p[0]);
      margins.forEach((m, i) => {
        const e = error + m.seconds;
        if (e < 0) { m.early++; early[i]++; }
        if (e < -5) m.earlyOver5++;
        if (e > 5) m.lateOver5++;
        if (e > 15) m.lateOver15++;
      });
    }
    if (count) margins.forEach((m,i) => m.fileEarly.push(early[i] / count));
  }
  return {
    files: files.length, blocks: errors.length,
    linearMinusFirstPresentationSeconds: stats(errors),
    containerLinearMinusFirstPresentationSeconds: stats(containerErrors),
    blockPresentationSpanSeconds: stats(spans),
    nextSyncAfterBlockMinusFirstPresentationSeconds: stats(nextKey),
    presentationMinusDecodeSeconds: stats(decodeLead),
    margins: margins.map(m => ({ seconds: m.seconds,
      earlyPct: round(100 * m.early / errors.length), earlyOver5Pct: round(100 * m.earlyOver5 / errors.length),
      lateOver5Pct: round(100 * m.lateOver5 / errors.length), lateOver15Pct: round(100 * m.lateOver15 / errors.length),
      equalFileEarlyPct: round(100 * m.fileEarly.reduce((s,v) => s+v,0) / m.fileEarly.length),
    })),
  };
}
const targetPairs = [[4598,2],[2502,2],[3214,16],[1314,16],[1314,14],[6776,12],[5396,16],[5396,5],[8935,8],[8935,2]];
const result = {
  schemaVersion: 1, blockBytes: B,
  definitions: {
    block: 'Zero-based; block 0 and blocks without visible audio/video samples excluded from aggregate statistics.',
    firstPresentation: 'Earliest visible edited audio/video PTS touching the block, including samples crossing a block boundary; not measured player buffer exhaustion.',
    error: 'Catalog duration * block start / total bytes minus first presentation time; positive means the linear estimate is later.',
    margin: 'Added to the linear estimate; percentages count hypothetical thresholds over file blocks, not actual requests, failure probabilities or recovery rates.',
    quantiles: 'Sorted values at floor(q * count), capped at count - 1.',
  },
  inventory: { scanned: rows.length, parsed: valid.length, failed: rows.length - valid.length,
    catalogMatched: matched.length, progressive: valid.filter(r => r.progressive).length,
    bytes: valid.reduce((s,r) => s+r.bytes,0), metadataBytesRead: valid.reduce((s,r) => s+r.bytesRead,0),
    nonzeroCatalogStartFiles: matched.filter(r => r.nonzeroCatalogStarts).length,
    ambiguousCatalogDurationFiles: matched.filter(r => r.catalogSeconds !== r.catalogMaxSeconds).length },
  catalogMinusContainerSeconds: stats(matched.map(r => r.catalogSeconds-r.containerSeconds)),
  durationOutliersOver2Seconds: matched.filter(r => Math.abs(r.catalogSeconds-r.containerSeconds)>2)
    .map(r => ({songs:r.songIDs,catalog:r.catalogSeconds,container:r.containerSeconds})),
  allMatched: summarize(matched),
  progressiveMatched: summarize(matched.filter(r => r.progressive)),
  tailMetadataMatched: summarize(matched.filter(r => !r.progressive)),
  maxKeyframeGapPerFileSeconds: stats(valid.map(r => r.keyframeGapMax)),
  targets: targetPairs.map(([song,block]) => {
    const r = matched.find(r => r.songIDs.includes(song));
    const p = r.blocks[block], linear = r.catalogSeconds*block*B/r.bytes;
    return {song,block,bytes:r.bytes,catalogSeconds:r.catalogSeconds,containerSeconds:r.containerSeconds,
      linearSeconds:round(linear),firstAVSeconds:p[0],firstDecodeSeconds:p[1],lastAVEndSeconds:p[2],
      firstVideoSeconds:p[3],firstAudioSeconds:p[5],nextSyncAfterBlockSeconds:p[7],errorSeconds:round(linear-p[0])};
  }),
};
if (playback) {
  const history = JSON.parse(fs.readFileSync(playback, 'utf8'));
  result.liveThresholdReplay = history.cases.filter(c => c.id !== 'boundary5396').flatMap(c => {
    const file = matched.find(r => r.songIDs.includes(Number(c.song)));
    const block = c.missingRange[0] / B;
    if (!Number.isInteger(block) || !file) throw new Error('unmatched live case');
    const first = file.blocks[block][0], linear = file.catalogSeconds * c.missingRange[0] / file.bytes;
    return c.decisions.filter(d => d.needByT !== null).map(d => ({
      case: c.id, song: Number(c.song), block,
      requestT: d.t, linearNeedByT: d.needByT,
      indexFirstUseT: round(d.needByT - linear + first),
      requestMinusIndexFirstUseSeconds: round(d.t - d.needByT + linear - first),
      thresholdOnlyReject: [-2,0,2,5].map(margin => ({margin, reject: d.t > d.needByT + margin})),
    }));
  });
}
fs.writeFileSync(output, JSON.stringify(result,null,2)+'\n', {flag:'wx'});
console.log(JSON.stringify({inventory:result.inventory,errors:result.allMatched.linearMinusFirstPresentationSeconds,margins:result.allMatched.margins,targets:result.targets},null,2));
