// Dependency-free freshness check for builds using the committed browser bundle.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import {checkDashboard} from './dashboard-source.mjs';

export const webuiOutputs = ['internal/console/assets/webui/app.js', 'internal/console/assets/webui/app.css', 'internal/console/assets/webui/software-license.txt','internal/console/assets/webui/third-party-notices.txt','internal/console/assets/webui/webui-notices.txt','docs/licenses/WEBUI-NOTICES.txt','internal/console/assets/webui/zashboard-notices.txt'];
export function webuiManifest(root) {
  const walk = dir => fs.readdirSync(path.join(root, dir), {withFileTypes:true}).flatMap(e => {
    const name = dir + '/' + e.name;
    if (e.isSymbolicLink()) throw Error('Unexpected Web UI symlink: ' + name);
    return e.isDirectory() ? walk(name) : [name];
  });
  const covers = walk('internal/console/assets/webui/category-covers');
  // Runtime artwork has a total budget; lossless editing masters stay outside the executable.
  if (covers.reduce((bytes, name) => bytes + fs.statSync(path.join(root, name)).size, 0) > 1024 * 1024) {
    throw Error('Category cover assets exceed the 1 MiB runtime budget');
  }
  const inputs = [...walk('webui/src'), ...covers, 'LICENSE','THIRD-PARTY-NOTICES.txt','docs/licenses/ZASHBOARD-NOTICES.txt','docs/research/cache-priority/study-data.json','docs/research/category-cover-assets-20261112.json','webui/build.mjs', 'webui/package.json', 'webui/pnpm-lock.yaml', 'webui/pnpm-workspace.yaml', 'webui/tsconfig.json', 'docs/licenses/fluentui-system-icons-LICENSE', 'scripts/check-webui.mjs','scripts/prepare-category-covers.py'];
  // Match Git LF text on Windows and hash image bytes without decoding them.
  const hashes = names => Object.fromEntries(names.sort().map(name => [name, crypto.createHash('sha256').update(name.endsWith('.webp')?fs.readFileSync(path.join(root,name)):fs.readFileSync(path.join(root,name),'utf8').replaceAll('\r\n','\n')).digest('hex')]));
  return {schemaVersion:1, inputs:hashes(inputs), outputs:hashes([...webuiOutputs])};
}
export function checkWebUI(root) {
  checkDashboard(root);
  const expected=JSON.parse(fs.readFileSync(path.join(root,'webui/build-manifest.json'),'utf8'));
  if (JSON.stringify(expected)!==JSON.stringify(webuiManifest(root))) throw Error('Stale embedded Web UI. Run pnpm --dir webui build and commit its assets, notices and manifest.');
}
if (process.argv[1] && path.resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try {checkWebUI(path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..'));console.log('Embedded Web UI inputs and outputs verified.');}
  catch(e){console.error(e.message);process.exitCode=1;}
}
