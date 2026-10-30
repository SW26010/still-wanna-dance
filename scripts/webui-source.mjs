// pnpm's content-addressable store preserves complete locked npm packages.
import fs from 'node:fs';
import path from 'node:path';
import {execFileSync} from 'node:child_process';
import {checkWebUI} from './check-webui.mjs';

export function pnpm(args,cwd,env={}) {
  const childEnv={...process.env,...env};
  // The release explicitly checks its pnpm version. Do not let an upstream
  // packageManager field silently download or switch to a different binary.
  const overridden=new Set(['pnpm_config_pm_on_fail','corepack_enable_project_spec','corepack_enable_network','corepack_default_to_latest','corepack_env_file']);
  for(const key of Object.keys(childEnv))if(overridden.has(key.toLowerCase()))delete childEnv[key];
  Object.assign(childEnv,{pnpm_config_pm_on_fail:'ignore',COREPACK_ENABLE_PROJECT_SPEC:'0',COREPACK_ENABLE_NETWORK:'0',COREPACK_DEFAULT_TO_LATEST:'0',COREPACK_ENV_FILE:'0',CI:'true',SWD_PNPM_ARGS:JSON.stringify(args)});
  const options={cwd,encoding:'utf8',windowsHide:true,maxBuffer:32*1024*1024,env:childEnv};
  // Arguments are JSON data, never interpolated into shell source.
  try {
    return process.platform==='win32'
      ? execFileSync('pwsh',['-NoProfile','-Command','$items = @($env:SWD_PNPM_ARGS | ConvertFrom-Json); & pnpm @items; exit $LASTEXITCODE'],options)
      : execFileSync('pnpm',args,options);
  } catch(error) {
    throw new Error(`pnpm ${args[0]} failed:\n${error.stdout||''}${error.stderr||''}`,{cause:error});
  }
}
function checkManager(source) {
  const packageManager=JSON.parse(fs.readFileSync(path.join(source,'webui/package.json'),'utf8')).packageManager;
  if(packageManager!=='pnpm@'+pnpm(['--version'],source).trim())throw Error('Install the exact packageManager from webui/package.json before source packaging/rebuild.');
  return packageManager;
}
export function collectFrontend(source,bundle,work) {
  if(!fs.existsSync(path.join(source,'webui')))return null; // Minimal Go tooling fixtures.
  checkWebUI(source);
  const packageManager=checkManager(source);
  collectPackageStore(path.join(source,'webui'),path.join(bundle,'frontend-store'),path.join(work,'webui-fetch'),path.join(work,'frontend-registry-cache'));
  return {packageManager,store:'frontend-store'};
}
export function collectPackageStore(packageRoot,store,staging,cache,policyArgs=[],workspaceAppend='') {
  fs.mkdirSync(staging);
  for(const name of ['package.json','pnpm-lock.yaml','pnpm-workspace.yaml'])fs.copyFileSync(path.join(packageRoot,name),path.join(staging,name));
  if(workspaceAppend) {
    const workspace=path.join(staging,'pnpm-workspace.yaml');
    if(/^supportedArchitectures\s*:/m.test(fs.readFileSync(workspace,'utf8')))throw Error('Review upstream supportedArchitectures before overriding it');
    fs.appendFileSync(workspace,workspaceAppend);
  }
  // Fetch can create a virtual store in staging; only the portable CAS is shipped.
  // The trust check also collects full metadata for every locked package. The
  // default age check can use online attestations that are unavailable offline.
  pnpm(['fetch','--ignore-scripts','--trust-policy','no-downgrade',...policyArgs,'--store-dir',store],staging,{pnpm_config_cache_dir:cache});
  // Ship metadata inputs, never the machine-local cached policy verdict.
  fs.cpSync(path.join(cache,'v11'),path.join(store,'registry-cache/v11'),{recursive:true});
  cleanFrontendStore(store);
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
  pnpm(['install','--offline','--frozen-lockfile','--ignore-scripts','--store-dir',store],path.join(checkout,'webui'),{pnpm_config_cache_dir:path.join(store,'registry-cache')});
  // Regenerate in isolation and byte-compare assets/notices; never trust just a manifest.
  pnpm(['check'],path.join(checkout,'webui'));
}
