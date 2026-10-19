import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { withTemp, run, sha256 } from './release-source.mjs';

const scripts = path.dirname(fileURLToPath(import.meta.url));
const quote = value => "'" + value.replaceAll("'", "''") + "'";
for (const scenario of ['unknown-file', 'unknown-directory', 'bad-checksum']) {
  test(`Portable preflight rejects ${scenario} before starting an executable`, {skip:process.platform !== 'win32'}, () => withTemp(temp => {
    const input = path.join(temp, 'input'), folder = path.join(input, 'SWD');
    for (const relative of JSON.parse(fs.readFileSync(path.join(scripts,'portable-files.json')))) {
      const file = path.join(folder,relative); fs.mkdirSync(path.dirname(file),{recursive:true}); fs.writeFileSync(file,'not executable: must never run');
    }
    if (scenario==='unknown-file') fs.writeFileSync(path.join(folder,'README.txt'),'obsolete delivery');
    if (scenario==='unknown-directory') fs.mkdirSync(path.join(folder,'data'));
    const archive = path.join(temp,'portable.zip');
    run('pwsh',['-NoProfile','-NonInteractive','-Command',`[IO.Compression.ZipFile]::CreateFromDirectory(${quote(input)},${quote(archive)})`]);
    fs.writeFileSync(archive+'.sha256',`${scenario==='bad-checksum'?'0'.repeat(64):sha256(archive)}  portable.zip\n`);
    const evidence = path.join(temp,'evidence');
    let failure;
    try { run('pwsh',['-NoProfile','-File',path.join(scripts,'test-portable.ps1'),'-Zip',archive,'-EvidenceDir',evidence]); }
    catch (error) { failure = String(error.stderr); }
    assert.match(failure || '', scenario==='bad-checksum' ? /checksum mismatch/ : /Unexpected Portable ZIP|Empty or unknown ZIP/);
    for (const directory of fs.readdirSync(evidence)) assert.deepEqual(fs.readdirSync(path.join(evidence,directory)),[],'untrusted layout was extracted');
  }));
}
