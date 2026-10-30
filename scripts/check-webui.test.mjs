import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {checkWebUI,webuiManifest,webuiOutputs} from './check-webui.mjs';
import {cleanFrontendStore} from './webui-source.mjs';

test('freshness rejects changed, added, removed source and modified output; accepts Git CRLF checkout',()=>{
  const temp=fs.mkdtempSync(path.join(os.tmpdir(),'swd-webui-'));
  try {
    const files=['LICENSE','THIRD-PARTY-NOTICES.txt','docs/licenses/ZASHBOARD-NOTICES.txt','webui/src/main.tsx','webui/build.mjs','webui/package.json','webui/pnpm-lock.yaml','webui/pnpm-workspace.yaml','webui/tsconfig.json','docs/licenses/fluentui-system-icons-LICENSE','scripts/check-webui.mjs',...webuiOutputs];
    for(const name of files){fs.mkdirSync(path.dirname(path.join(temp,name)),{recursive:true});fs.writeFileSync(path.join(temp,name),'original\n');}
    fs.writeFileSync(path.join(temp,'webui/build-manifest.json'),JSON.stringify(webuiManifest(temp)));
    checkWebUI(temp);
    const source=path.join(temp,'webui/src/main.tsx');
    fs.writeFileSync(source,'original\r\n');checkWebUI(temp);
    fs.writeFileSync(source,'changed');assert.throws(()=>checkWebUI(temp),/Stale/);fs.writeFileSync(source,'original\n');
    const added=path.join(temp,'webui/src/new.tsx');fs.writeFileSync(added,'new');assert.throws(()=>checkWebUI(temp),/Stale/);fs.unlinkSync(added);
    fs.unlinkSync(source);assert.throws(()=>checkWebUI(temp),/Stale/);fs.writeFileSync(source,'original\n');
    fs.writeFileSync(path.join(temp,webuiOutputs[0]),'modified');assert.throws(()=>checkWebUI(temp),/Stale/);
  } finally {
    const target=path.resolve(temp);
    if(path.dirname(target)!==path.resolve(os.tmpdir())||!path.basename(target).startsWith('swd-webui-'))throw Error('Unsafe cleanup path');
    fs.rmSync(target,{recursive:true,force:true});
  }
});

test('source store cleanup removes project junctions without traversing their targets',()=>{
  const temp=fs.mkdtempSync(path.join(os.tmpdir(),'swd-webui-'));
  try {
    const store=path.join(temp,'store'),projects=path.join(store,'v11/projects'),target=path.join(temp,'local-project');
    fs.mkdirSync(projects,{recursive:true});fs.mkdirSync(target);fs.writeFileSync(path.join(target,'keep.txt'),'keep');
    fs.symlinkSync(target,path.join(projects,'project'),process.platform==='win32'?'junction':'dir');
    cleanFrontendStore(store);
    assert.equal(fs.existsSync(projects),false);assert.equal(fs.readFileSync(path.join(target,'keep.txt'),'utf8'),'keep');
  } finally {
    const target=path.resolve(temp);
    if(path.dirname(target)!==path.resolve(os.tmpdir())||!path.basename(target).startsWith('swd-webui-'))throw Error('Unsafe cleanup path');
    fs.rmSync(target,{recursive:true,force:true});
  }
});
