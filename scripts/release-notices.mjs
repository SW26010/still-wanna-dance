import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { executableInfo, readJSON } from './release-source.mjs';

export function checkNotices(text, review, modules) {
  const normalized = text.replace(/\r\n/g, '\n');
  const hash = crypto.createHash('sha256').update(normalized).digest('hex');
  if (review.schemaVersion !== 1 || hash !== review.noticesSHA256) throw Error('Third-party notice contents differ from the reviewed inventory');
  for (const mod of modules) {
    if (!review.modules.some(m => m.path === mod.path && m.version === mod.version && m.sum === mod.sum && m.licenseFiles.length > 0)) throw Error(`Missing reviewed license identity: ${mod.path}@${mod.version}`);
    if (!normalized.split('\n').includes(`Module: ${mod.path} ${mod.version}`)) throw Error(`Missing notice text: ${mod.path}`);
  }
}

export function validateNotices(root, modules) {
  checkNotices(fs.readFileSync(path.join(root, 'THIRD-PARTY-NOTICES.txt'), 'utf8'), readJSON(path.join(root, 'docs/licenses/reviewed-modules.json')), modules);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
    const info = executableInfo(process.argv[2]);
    if (info.entry !== 'still-wanna-dance/cmd/still-wanna-dance-console') throw Error('Expected the desktop executable');
    validateNotices(root, info.modules);
    console.log(`Reviewed notices cover ${info.modules.length} executable modules.`);
  } catch (e) { console.error(e.message); process.exitCode = 1; }
}
