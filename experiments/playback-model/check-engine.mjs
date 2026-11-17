import fs from 'node:fs';
import path from 'node:path';
import {spawn} from 'node:child_process';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
const [binaryArg,mediaArg,outputArg]=process.argv.slice(2);
if(!binaryArg||!mediaArg||!outputArg)throw Error('usage: check-engine.mjs <binary> <4598-media-file> <new-output-directory>');
const binary=path.resolve(binaryArg),media={file:path.resolve(mediaArg)},root=path.resolve(outputArg);
assert.equal(fs.statSync(media.file).size,35169662,'requires recorded 4598 media size');
assert(fs.existsSync(binary),'binary missing');
fs.mkdirSync(root,{recursive:false});
const pause=ms=>new Promise(r=>setTimeout(r,ms));
const results=[];
for(const enabled of [false,true]){
 const dir=path.join(root,`model-integration-${enabled}-${Date.now()}`);fs.mkdirSync(dir,{recursive:true});const gameDir=path.join(dir,'game');fs.mkdirSync(gameDir);const game=path.join(gameDir,'output_log_2000-01-01_00-00-00.txt');fs.writeFileSync(game,'');
 const log=fs.openSync(path.join(dir,'process.log'),'w');
 const child=spawn(binary,['-test.run','^TestIssue51LabServer$','-test.v','-test.timeout','30s'],{windowsHide:true,env:{...process.env,LAB51_ROOT:dir,LAB51_FILE:media.file,LAB51_MODE:'',LAB51_LISTEN:'127.0.0.1:0',LAB51_RELEASE_SECONDS:'66',LAB51_FORCE_FRESH:'1',LAB51_WARM_BLOCKS:'2',LAB51_SINGLE_HOLE:'1',LAB51_HOLE_BLOCKS:'1',LAB51_SONG:'4598',LAB51_MEDIA_SECONDS:'133',LAB51_MODEL_MARGIN_SECONDS:'5',LAB51_MODEL_ENABLED:enabled?'1':'0',LAB51_GAME_LOG_DIR:gameDir,TEMP:dir,TMP:dir,GOTMPDIR:dir},stdio:['ignore',log,log]});
 const done=new Promise(resolve=>child.once('exit',code=>resolve(code)));
 const events=()=>fs.existsSync(path.join(dir,'engine.jsonl'))?fs.readFileSync(path.join(dir,'engine.jsonl'),'utf8').split(/\r?\n/).filter(Boolean).map(JSON.parse):[];
 try{
  const readyFile=path.join(dir,'ready.json');for(let i=0;!fs.existsSync(readyFile)&&i<100;i++)await pause(50);const ready=JSON.parse(fs.readFileSync(readyFile));
  const d=new Date(),pad=n=>String(n).padStart(2,'0');const at=`${d.getFullYear()}.${pad(d.getMonth()+1)}.${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  fs.appendFileSync(game,[`${at} Debug - [VideoQueueManager] [LogOtherPlayerAndDeviceType] Player anonymous, isLocal = True, isOwner = False, isMaster = False`,`${at} Debug - [USharpVideo (WannaDance)] OnVideoStart: Started video: http://api.udon.dance/Api/Songs/play?id=4598, since owner is playing`,`${at} Debug - [USharpVideo (WannaDance)] Syncing video to 20, current time = 0, offset = 20`].join('\n')+'\n');
  await pause(3200);
  const url=`http://${ready.address}/Api/Songs/play?id=4598`,headers={'user-agent':'NSPlayer/model-integration'};
  const first=await fetch(url,{headers:{...headers,range:'bytes=0-'}});assert.equal(first.status,206);const reader=first.body.getReader();let bytes=0;while(bytes<2097152){const part=await reader.read();assert(!part.done);bytes+=part.value.length;}assert.equal(bytes,2097152);await reader.cancel();
  for(let i=0;i<80&&!events().some(e=>e.msg==='lab_model_handoff');i++)await pause(25);
  const before=events().filter(e=>e.event_name==='request_started').length;
  const retry=await fetch(url,{headers:{...headers,range:'bytes=2088960-35169661'}});assert.equal(retry.status,enabled?504:206);
  if(enabled){assert.equal(await retry.text(),'Gateway Timeout\n');assert.equal(events().filter(e=>e.event_name==='request_started').length,before,'504 must not enter SWD');}
  else{await retry.body.cancel();}
  if(enabled){const again=await fetch(url,{headers:{...headers,range:'bytes=2088960-35169661'}});assert.equal(again.status,206);await again.body.cancel();}
  await pause(100);const e=events(),handoffs=e.filter(e=>e.msg==='lab_model_handoff').map(({first,last,seconds})=>({first,last,seconds}));
  assert(handoffs.some(h=>h.seconds===2));assert.equal(handoffs.some(h=>h.seconds===10),enabled);
  assert.equal(e.filter(e=>e.msg==='lab_model_admission'&&e.reject).length,enabled?1:0);
  results.push({enabled,passed:true,firstVideoBytes:bytes,retryStatus:retry.status,rejectedBeforeSWDDemand:enabled?true:null,tenSecondHandoffObserved:handoffs.some(h=>h.seconds===10),handoffs});
 }finally{fs.writeFileSync(path.join(dir,'stop'),'integration check complete');assert.equal(await done,0);fs.closeSync(log);}
}
const proof={binarySHA256:crypto.createHash('sha256').update(fs.readFileSync(binary)).digest('hex'),controlledHTTPAndLogOnly:true,gameBehaviorVerified:false,results};
fs.writeFileSync(path.join(root,'model-engine-check.json'),JSON.stringify(proof,null,2)+'\n');console.log(JSON.stringify(proof));
