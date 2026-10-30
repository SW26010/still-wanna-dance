// The shipped ZIP is an unmodified upstream release. Its full pinned source and
// locked npm inputs travel alongside SWD's corresponding source for offline builds.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {pnpm,collectPackageStore} from './webui-source.mjs';

const hash=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
export function dashboardRelease(source) {
  const file=path.join(source,'internal/dashboard/release.json');
  return fs.existsSync(file)?JSON.parse(fs.readFileSync(file,'utf8')):null;
}
export function checkDashboard(source) {
  const release=dashboardRelease(source);
  if(release && hash(path.join(source,'internal/dashboard/assets/zashboard.zip'))!==release.assetSHA256)throw Error('Unreviewed zashboard distribution');
  if(release) {
    const inventory=JSON.parse(fs.readFileSync(path.join(source,'docs/licenses/zashboard-packages.json')));
    const notices=fs.readFileSync(path.join(source,'docs/licenses/ZASHBOARD-NOTICES.txt'),'utf8').replaceAll('\r\n','\n');
    if(inventory.revision!==release.revision || crypto.createHash('sha256').update(notices).digest('hex')!==inventory.noticesSHA256)throw Error('Dashboard notices need review');
  }
  return release;
}
function manager(release,cwd) {
  if('pnpm@'+pnpm(['--version'],cwd).trim()!==release.rebuildPackageManager)throw Error('Install dashboard release.json rebuildPackageManager');
}
const trustArgs=release=>(release.trustPolicyExclude||[]).map(id=>'--trust-policy-exclude='+id);
// The upstream workspace requests only the builder's platform. Expand it only
// in the disposable fetch staging directory; shipped upstream source is intact.
export const dashboardArchitectures='\nsupportedArchitectures:\n  os: [win32, linux, darwin]\n  cpu: [x64, arm64]\n  libc: [glibc, musl]\n';
export function collectDashboard(source,bundle,work) {
  const release=checkDashboard(source);if(!release)return null;
  manager(release,source);
  const archive=path.join(work,'zashboard-source.zip');
  execFileSync(process.execPath,[path.join(source,'scripts/fetch-source-input.mjs'),release.sourceURL,archive,release.sourceSHA256],{windowsHide:true,stdio:'inherit'});
  const unpack=path.join(work,'dashboard-unpack');fs.mkdirSync(unpack);
  execFileSync('tar',['-xf',archive,'-C',unpack],{windowsHide:true});
  const checkout=path.join(bundle,'dashboard/source');fs.mkdirSync(path.dirname(checkout));
  fs.renameSync(path.join(unpack,release.sourcePrefix),checkout);
  manager(release,checkout);
  collectPackageStore(checkout,path.join(bundle,'dashboard/store'),path.join(work,'dashboard-fetch'),path.join(work,'dashboard-registry-cache'),trustArgs(release),dashboardArchitectures);
  return {revision:release.revision,source:'dashboard/source',store:'dashboard/store',packageManager:release.rebuildPackageManager};
}
export function verifyDashboardBundle(source,bundle,manifest,seen) {
  const release=checkDashboard(source);if(!release)return;
  if(manifest?.revision!==release.revision || manifest?.packageManager!==release.rebuildPackageManager || manifest?.source!=='dashboard/source' || manifest?.store!=='dashboard/store')throw Error('Missing dashboard source identity');
  for(const file of ['package.json','pnpm-lock.yaml','pnpm-workspace.yaml','vite.config.ts','src/main.ts','LICENSE'])if(!seen.has('dashboard/source/'+file))throw Error('Missing dashboard source: '+file);
  if(![...seen].some(p=>p.startsWith('dashboard/store/registry-cache/v11/metadata-full/')))throw Error('Missing dashboard offline dependency inputs');
  if(seen.has('dashboard/store/registry-cache/lockfile-verified.jsonl'))throw Error('Dashboard source contains cached policy verdicts');
  const pkg=JSON.parse(fs.readFileSync(path.join(bundle,'dashboard/source/package.json'),'utf8'));
  if('v'+pkg.version!==release.version)throw Error('Dashboard source version mismatch');
}
export function rebuildDashboard(source,bundle,work) {
  const release=checkDashboard(source);if(!release)return;
  manager(release,source);
  const checkout=path.join(work,'dashboard-rebuild'),store=path.join(work,'dashboard-store');
  fs.cpSync(path.join(bundle,'dashboard/source'),checkout,{recursive:true});
  fs.cpSync(path.join(bundle,'dashboard/store'),store,{recursive:true});
  manager(release,checkout);
  pnpm(['install','--offline','--frozen-lockfile','--ignore-scripts','--trust-policy','no-downgrade',...trustArgs(release),'--store-dir',store],checkout,{pnpm_config_cache_dir:path.join(store,'registry-cache')});
  // Do not let upstream's optional git label accidentally refer to SWD's Git.
  // No source patches: the release itself uses an empty commit suffix.
  pnpm(['run','build'],checkout,{FONT:'none',GIT_DIR:path.join(work,'no-dashboard-git')});
  for(const name of ['index.html','sw.js','THIRD_PARTY_NOTICES.md'])if(!fs.existsSync(path.join(checkout,'dist',name)))throw Error('Incomplete dashboard rebuild: '+name);
  // Toolchain-dependent chunk hashes are not asserted equal to the release ZIP.
}
