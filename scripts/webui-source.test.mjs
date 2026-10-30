import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import crypto from 'node:crypto';
import {pnpm} from './webui-source.mjs';
import {rebuildDashboard} from './dashboard-source.mjs';

test('source pnpm commands explicitly avoid project-driven version switching',()=>{
  const temp=fs.mkdtempSync(path.join(os.tmpdir(),'swd-pnpm-policy-'));
  const previousPath=process.env.PATH;
  const previousPolicy=Object.fromEntries(Object.entries(process.env).filter(([key])=>key.toLowerCase()==='pnpm_config_pm_on_fail'));
  try {
    // Simulate a normal launcher that does not add any local policy override.
    fs.writeFileSync(path.join(temp,'package.json'),JSON.stringify({packageManager:'pnpm@11.20.0'}));
    fs.writeFileSync(path.join(temp,'probe.mjs'),`import fs from 'node:fs';
if(JSON.parse(fs.readFileSync('package.json')).packageManager!=='pnpm@11.20.0'||process.env.pnpm_config_pm_on_fail!=='ignore'||process.env.COREPACK_ENABLE_PROJECT_SPEC!=='0'||process.env.COREPACK_ENABLE_NETWORK!=='0'||process.env.COREPACK_DEFAULT_TO_LATEST!=='0'||process.env.COREPACK_ENV_FILE!=='0')process.exit(42);
const args=process.argv.slice(2);console.log(args[0]==='--version'?'11.25.0':args.join(' '));\n`);
    const launcher=process.platform==='win32'?'pnpm.cmd':'pnpm';
    fs.writeFileSync(path.join(temp,launcher),process.platform==='win32'
      ? '@echo off\r\nnode "%~dp0probe.mjs" %*\r\nexit /b %ERRORLEVEL%\r\n'
      : '#!/bin/sh\nexec node "$(dirname "$0")/probe.mjs" "$@"\n', {mode:0o755});
    process.env.PATH=temp+path.delimiter+previousPath;
    process.env.PNPM_CONFIG_PM_ON_FAIL='download';
    for(const args of [['--version'],['fetch'],['install','--offline'],['run','build']]) {
      assert.equal(pnpm(args,temp).trim(),args[0]==='--version'?'11.25.0':args.join(' '));
    }
    // A different installed pnpm must fail before any source rebuild begins.
    fs.mkdirSync(path.join(temp,'internal/dashboard/assets'),{recursive:true});
    fs.mkdirSync(path.join(temp,'docs/licenses'),{recursive:true});
    const asset=Buffer.from('fixture zip'),notices='fixture notice\n';
    const sha=data=>crypto.createHash('sha256').update(data).digest('hex');
    fs.writeFileSync(path.join(temp,'internal/dashboard/assets/zashboard.zip'),asset);
    fs.writeFileSync(path.join(temp,'docs/licenses/ZASHBOARD-NOTICES.txt'),notices);
    fs.writeFileSync(path.join(temp,'docs/licenses/zashboard-packages.json'),JSON.stringify({revision:'fixture',noticesSHA256:sha(notices)}));
    fs.writeFileSync(path.join(temp,'internal/dashboard/release.json'),JSON.stringify({revision:'fixture',assetSHA256:sha(asset),rebuildPackageManager:'pnpm@11.24.0'}));
    assert.throws(()=>rebuildDashboard(temp,path.join(temp,'bundle'),path.join(temp,'work')),/rebuildPackageManager/);
  } finally {
    process.env.PATH=previousPath;
    for(const key of Object.keys(process.env))if(key.toLowerCase()==='pnpm_config_pm_on_fail')delete process.env[key];
    Object.assign(process.env,previousPolicy);
    const target=path.resolve(temp);
    if(path.dirname(target)!==path.resolve(os.tmpdir())||!path.basename(target).startsWith('swd-pnpm-policy-'))throw Error('Unsafe cleanup path');
    fs.rmSync(target,{recursive:true,force:true});
  }
});
