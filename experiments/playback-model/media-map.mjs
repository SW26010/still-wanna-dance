// Offline index analysis only. No media decoding, rewriting, or product policy.
// Usage: node media-map.mjs <inventory.json> <mp4box.all.mjs> <new-output-dir> [workers]
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { Worker, isMainThread, parentPort, workerData } from 'node:worker_threads';

const BLOCK = 1048576;
const round = x => Number.isFinite(x) ? Math.round(x * 1e6) / 1e6 : null;

function editMapping(track, movieScale, duration) {
  const edits = track.edts?.elst?.entries ?? [];
  if (!edits.length) return { offset: 0, end: duration };
  let leading = 0;
  let active;
  for (const edit of edits) {
    if (edit.media_rate_integer !== 1 || edit.media_rate_fraction !== 0) {
      throw new Error('unsupported-edit-rate');
    }
    if (edit.media_time === -1 && !active) leading += edit.segment_duration / movieScale;
    else if (edit.media_time >= 0 && !active) active = edit;
    else throw new Error('unsupported-multiple-edits');
  }
  if (!active) throw new Error('empty-edit-list');
  return {
    offset: leading - active.media_time / track.mdia.mdhd.timescale,
    end: leading + active.segment_duration / movieScale,
  };
}

export function analyzeFile(item, createFile) {
  const before = fs.statSync(item.file);
  if (before.size !== item.bytes || before.mtimeMs !== item.mtime) throw new Error('file-changed');
  const file = createFile(false);
  let info, position = 0, bytesRead = 0;
  file.onReady = value => { info = value; };
  file.onError = () => { throw new Error('mp4-parse-error'); };
  const fd = fs.openSync(item.file, 'r');
  try {
    while (!info) {
      if (bytesRead >= 64 * BLOCK || position >= item.bytes) throw new Error('metadata-read-bound');
      const buffer = Buffer.alloc(Math.min(65536, item.bytes - position));
      const n = fs.readSync(fd, buffer, 0, buffer.length, position);
      if (!n) throw new Error('unexpected-eof');
      const array = buffer.buffer.slice(buffer.byteOffset, buffer.byteOffset + n);
      array.fileStart = position;
      const next = file.appendBuffer(array);
      bytesRead += n;
      if (!(next > position)) throw new Error('metadata-no-progress');
      position = next;
    }
  } finally { fs.closeSync(fd); }
  if (info.isFragmented) throw new Error('fragmented-file-not-covered');
  const duration = info.duration / info.timescale;
  if (!(duration > 0 && Number.isFinite(duration))) throw new Error('invalid-duration');
  const tracks = info.tracks.filter(t => ['video', 'audio'].includes(t.type));
  if (tracks.filter(t => t.type === 'video').length !== 1 || tracks.filter(t => t.type === 'audio').length > 1) {
    throw new Error('ambiguous-av-track-selection');
  }
  // Per block: first display time, first decode time, last display end,
  // first/last video display time, first/last audio display time.
  const blocks = Array.from({ length: Math.ceil(item.bytes / BLOCK) }, () =>
    [Infinity, Infinity, -Infinity, Infinity, -Infinity, Infinity, -Infinity]);
  const keys = [];
  let sampleCount = 0, leadingSamples = 0;
  for (const track of tracks) {
    const actual = file.getTrackById(track.id);
    const mapping = editMapping(actual, info.timescale, duration);
    const samples = file.getTrackSamplesInfo(track.id);
    if (!samples?.length) throw new Error('empty-av-track');
    for (const sample of samples) {
      sampleCount++;
      const pts = sample.cts / track.timescale + mapping.offset;
      const dts = sample.dts / track.timescale + mapping.offset;
      const end = Math.min(mapping.end, pts + sample.duration / track.timescale);
      if (![pts, dts, end, sample.offset, sample.size].every(Number.isFinite) || sample.size <= 0 ||
          sample.offset < 0 || sample.offset + sample.size > item.bytes) throw new Error('invalid-sample');
      if (end <= 0 || pts >= mapping.end) { leadingSamples++; continue; }
      if (track.type === 'video' && sample.is_sync) keys.push({ offset: sample.offset, pts: Math.max(0, pts) });
      for (let b = Math.floor(sample.offset / BLOCK); b <= Math.floor((sample.offset + sample.size - 1) / BLOCK); b++) {
        const row = blocks[b], t = Math.max(0, pts);
        row[0] = Math.min(row[0], t); row[1] = Math.min(row[1], dts); row[2] = Math.max(row[2], end);
        const column = track.type === 'video' ? 3 : 5;
        row[column] = Math.min(row[column], t); row[column + 1] = Math.max(row[column + 1], end);
      }
    }
  }
  keys.sort((a, b) => a.offset - b.offset);
  let cursor = 0;
  // A sync sample whose entire starting position lies after the block is a
  // candidate later random-access point, not a claim about the game's seek choice.
  for (let b = 0; b < blocks.length; b++) {
    while (cursor < keys.length && keys[cursor].offset < Math.min(item.bytes, (b + 1) * BLOCK)) cursor++;
    blocks[b].push(cursor < keys.length ? keys[cursor].pts : Infinity);
  }
  const after = fs.statSync(item.file);
  if (after.size !== before.size || after.mtimeMs !== before.mtimeMs) throw new Error('file-changed-during-read');
  const catalog = item.songs.filter(s => s.byte_size === item.bytes && s.end > 0);
  const times = keys.map(k => k.pts).sort((a, b) => a - b);
  const gaps = times.slice(1).map((t, i) => t - times[i]).sort((a, b) => a - b);
  return {
    key: item.key, songIDs: catalog.map(s => s.id), bytes: item.bytes, bytesRead,
    catalogSeconds: catalog.length ? Math.min(...catalog.map(s => s.end)) : null,
    catalogMaxSeconds: catalog.length ? Math.max(...catalog.map(s => s.end)) : null,
    nonzeroCatalogStarts: catalog.filter(s => s.start > 0).length,
    containerSeconds: duration, progressive: info.isProgressive, sampleCount, leadingSamples,
    codecs: tracks.map(t => ({ type: t.type, codec: t.codec })),
    keyframeGapP50: round(gaps[Math.floor(gaps.length * .5)]),
    keyframeGapP95: round(gaps[Math.floor(gaps.length * .95)]),
    keyframeGapMax: round(gaps.at(-1)),
    blocks: blocks.map(row => row.map(round)),
  };
}

if (!isMainThread) {
  const { createFile } = await import(pathToFileURL(workerData.module).href);
  const fd = fs.openSync(workerData.output, 'wx');
  let ok = 0, failed = 0, bytesRead = 0;
  try {
    for (const item of workerData.files) {
      try {
        const result = analyzeFile(item, createFile);
        fs.writeSync(fd, JSON.stringify(result) + '\n'); ok++; bytesRead += result.bytesRead;
      } catch (error) {
        fs.writeSync(fd, JSON.stringify({ key: item.key, error: error.message }) + '\n'); failed++;
      }
      if ((ok + failed) % 250 === 0) parentPort.postMessage({ worker: workerData.id, ok, failed, bytesRead });
    }
  } finally { fs.closeSync(fd); }
  parentPort.postMessage({ worker: workerData.id, ok, failed, bytesRead, done: true });
} else if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
  const [input, module, output, count = '4'] = process.argv.slice(2);
  if (!input || !module || !output || !/^[1-8]$/.test(count)) throw new Error('usage: media-map.mjs <inventory.json> <mp4box.all.mjs> <new-output-dir> [1-8 workers]');
  const inventory = JSON.parse(fs.readFileSync(input, 'utf8').replace(/^\uFEFF/, ''));
  fs.mkdirSync(output, { recursive: false });
  const workers = Number(count);
  const progress = new Map();
  await Promise.all(Array.from({ length: workers }, (_, id) => new Promise((resolve, reject) => {
    const worker = new Worker(new URL(import.meta.url), { workerData: {
      id, files: inventory.files.filter((_, i) => i % workers === id),
      module: path.resolve(module), output: path.resolve(output, `part-${id}.jsonl`),
    }, resourceLimits: { maxOldGenerationSizeMb: 768 } });
    worker.on('message', value => {
      progress.set(id, value);
      fs.writeFileSync(path.join(output, 'progress.json'), JSON.stringify([...progress.values()], null, 2));
      console.log(JSON.stringify(value));
    });
    worker.on('error', reject);
    worker.on('exit', code => code === 0 ? resolve() : reject(new Error(`worker ${id} exit ${code}`)));
  })));
}
