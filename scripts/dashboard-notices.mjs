// Run against the pinned source after frozen-lockfile install. Rebuilds only
// the audit checkout; the shipped upstream ZIP is never rewritten.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {pathToFileURL,fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const checkout=path.resolve(process.argv[2]);
const release=JSON.parse(fs.readFileSync(path.join(root,'internal/dashboard/release.json')));
if('v'+JSON.parse(fs.readFileSync(path.join(checkout,'package.json'))).version!==release.version)throw Error('Wrong dashboard source');
process.chdir(checkout);process.env.FONT='none';process.env.GIT_DIR=path.join(checkout,'no-swd-git');
const {build}=await import(pathToFileURL(path.join(checkout,'node_modules/vite/dist/node/index.js')));
const ids=new Set();
const plugin=()=>({name:'swd-license-inventory',generateBundle(_,bundle){for(const out of Object.values(bundle))if(out.type==='chunk')for(const id of Object.keys(out.modules))ids.add(id);}});
await build({plugins:[plugin()],worker:{plugins:()=>[plugin()]}});
const packages=new Map();
function add(dir) {
  while(dir!==path.dirname(dir)) {
    const file=path.join(dir,'package.json');
    if(fs.existsSync(file)) {const pkg=JSON.parse(fs.readFileSync(file));if(pkg.name&&pkg.version){packages.set(pkg.name+'@'+pkg.version,{dir,pkg});return;}}
    dir=path.dirname(dir);
  }
}
for(const id of ids)if(id.includes('node_modules/'))add(path.dirname(id.replace(/^\0/,'')));
// Worker/PWA and generated CSS helpers can be emitted by separate builds.
// Conservatively include their installed packages, even if a helper is unused.
const store=path.join(checkout,'node_modules/.pnpm');
for(const entry of fs.readdirSync(store)) {
  const nm=path.join(store,entry,'node_modules');if(!fs.existsSync(nm))continue;
  for(const name of fs.readdirSync(nm))if(name.startsWith('workbox-')||['mmdb-lib','vite','tailwindcss','daisyui'].includes(name)){
    const dir=path.join(nm,name);if(!fs.lstatSync(dir).isSymbolicLink())add(dir);
  }
}
const walk=dir=>fs.readdirSync(dir,{withFileTypes:true}).flatMap(e=>{
  if(e.isSymbolicLink()||e.name==='node_modules')return [];
  const file=path.join(dir,e.name);return e.isDirectory()?walk(file):[file];
});
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const text=file=>fs.readFileSync(file,'utf8').replaceAll('\r\n','\n').trim();
let notices=`zashboard ${release.version} — unmodified dist-no-fonts distribution\nSource revision: ${release.revision}\nSource: ${release.sourceURL}\n\nThe no-fonts build still includes flag fonts. Assets keep their own licenses.\nDOMPurify is used under its Apache-2.0 option.\nPackage inventory covers source-build chunks and conservatively includes worker/PWA/CSS helpers.\nIt is not a claim that every listed package is in every loaded page.\n\n`;
notices+=text(path.join(checkout,'LICENSE'))+'\n\n'+text(path.join(checkout,'public/THIRD_PARTY_NOTICES.md'))+'\n';
const inventory=[];
for(const [id,{dir,pkg}] of [...packages].sort(([a],[b])=>a.localeCompare(b,'en'))) {
  const files=walk(dir).filter(f=>/^(licen[cs]e|copying|notice|copyright)([._-]|$)/i.test(path.basename(f))||/\.LICENSE\.txt$/i.test(f)).sort();
  if(!files.length)throw Error('Missing bundled license: '+id);
  const licenseFiles=[];notices+=`\n----------------------------------------\nPackage: ${id}\nLicense: ${pkg.license}\n`;
  for(const file of files){const relative=path.relative(dir,file).split(path.sep).join('/');notices+=`\nSource: ${relative}\n\n${text(file)}\n`;licenseFiles.push({path:relative,sha256:hash(fs.readFileSync(file))});}
  inventory.push({name:pkg.name,version:pkg.version,license:pkg.license,licenseFiles});
}
for(const file of walk(path.join(root,'docs/licenses/zashboard')).filter(f=>/\.(txt|md)$/.test(f)&&!f.endsWith('dependency-trust.md')).sort())notices+='\n----------------------------------------\nSupplement: '+path.basename(file)+'\n\n'+text(file)+'\n';
fs.writeFileSync(path.join(root,'docs/licenses/ZASHBOARD-NOTICES.txt'),notices);
fs.writeFileSync(path.join(root,'docs/licenses/zashboard-packages.json'),JSON.stringify({revision:release.revision,noticesSHA256:hash(notices),packages:inventory},null,2)+'\n');
console.log(`Reviewed dashboard notice inputs: ${packages.size} packages.`);
