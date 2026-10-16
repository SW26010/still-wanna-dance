// pnpm's content-addressable store preserves complete locked npm packages.
import fs from 'node:fs';
import path from 'node:path';
import {execFileSync} from 'node:child_process';
import {checkWebUI} from './check-webui.mjs';

function pnpm(args,cwd) {
  const options={cwd,encoding:'utf8',windowsHide:true,maxBuffer:32*1024*1024,env:{...process.env,CI:'true',SWD_PNPM_ARGS:JSON.stringify(args)}};
  // Arguments are JSON data, never interpolated into shell source.
  return process.platform==='win32'
    ? execFileSync('pwsh',['-NoProfile','-Command','$items = @($env:SWD_PNPM_ARGS | ConvertFrom-Json); & pnpm @items; exit $LASTEXITCODE'],options)
    : execFileSync('pnpm',args,options);
}
function checkManager(source) {
  const packageManager=JSON.parse(fs.readFileSync(path.join(source,'webui/package.json'),'utf8')).packageManager;
  if(packageManager!=='pnpm@'+pnpm(['--version'],source).trim())throw Error('Install the exact packageManager from webui/package.json before source packaging/rebuild.');
  return packageManager;
}
export function collectFrontend(source,bundle,work) {
  if(!fs.existsSync(path.join(source,'webui')))return null; // Minimal Go tooling fixtures.
  checkWebUI(source);
  const packageManager=checkManager(source), staging=path.join(work,'webui-fetch');
  fs.mkdirSync(staging);
  for(const name of ['package.json','pnpm-lock.yaml','pnpm-workspace.yaml'])fs.copyFileSync(path.join(source,'webui',name),path.join(staging,name));
  // Fetch can create a virtual store in staging; only the portable CAS is shipped.
  const store=path.join(bundle,'frontend-store');
  pnpm(['fetch','--ignore-scripts','--store-dir',store],staging);
  cleanFrontendStore(store);
  return {packageManager,store:'frontend-store'};
}
export function cleanFrontendStore(store) {
  const root=path.resolve(store),projects=path.resolve(root,'v11/projects');
  if(!projects.startsWith(root+path.sep))throw Error('Unsafe pnpm store path');
  // pnpm v11 tracks local projects with junctions. They are not package content
  // and must never enter a source archive or be traversed during copying.
  fs.rmSync(projects,{recursive:true,force:true});
}
export function rebuildFrontend(source,bundle,work) {
  if(!fs.existsSync(path.join(source,'webui')))return;
  checkWebUI(source);checkManager(source);
  const checkout=path.join(work,'webui-rebuild');
  fs.cpSync(source,checkout,{recursive:true});
  const store=path.join(work,'frontend-store');
  fs.cpSync(path.join(bundle,'frontend-store'),store,{recursive:true,filter:p=>path.relative(path.join(bundle,'frontend-store'),p).split(path.sep).join('/')!=='v11/projects'});
  pnpm(['install','--offline','--frozen-lockfile','--ignore-scripts','--store-dir',store],path.join(checkout,'webui'));
  // Regenerate in isolation and byte-compare assets/notices; never trust just a manifest.
  pnpm(['check'],path.join(checkout,'webui'));
}
