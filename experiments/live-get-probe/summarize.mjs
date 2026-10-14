// Produce allowlisted, relative-time evidence from this explicitly operated lab.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';

const root=path.resolve(process.argv[2]||'artifacts/acceptance-evidence/LIVE-20261014-01');
// Full traces stay in the ignored lab. Commit only concise, reviewed statistics.
const destination=path.resolve(process.argv[3]||path.join(root,'sanitized-full'));
const json=file=>JSON.parse(fs.readFileSync(path.join(root,file),'utf8').replace(/^\uFEFF/,''));
const readLines=file=>fs.existsSync(file)?fs.readFileSync(file,'utf8').split(/\r?\n/):[];
const app=[3,2,1,0].flatMap(n=>readLines(path.join(root,'data/logs/app.log'+(n?'.'+n:''))).flatMap((line,i)=>{try{return [{...JSON.parse(line),source:`app.log${n?'.'+n:''}`,line:i+1}]}catch{return []}}));
const actual=[
 {id:'baseline',song:8935,service:'7PRVM44ZV7DLEYEQ4OXNP4RAR6',rate:0,mode:'direct',relay:null},
 {id:'2m',song:2502,service:'GWVUHG6ORQPZXLPQXGMKEUYPB4',rate:2097152,mode:'proxy',relay:'relay-2m.jsonl',initialResolverContaminated:true},
 {id:'1m',song:4598,service:'VU5WTZYZSNWDL3364ZCC54SJVL',rate:1048576,mode:'proxy',relay:'relay-1m.jsonl'},
 {id:'half',song:10552,service:'X4UT6HAA7TCXXMCRFKKP6O5N6A',rate:524288,mode:'proxy',relay:'relay-half.jsonl'},
 {id:'stress',song:6776,service:'XR4NGIBYKTATFIQZCE7YO7HHF3',rate:131072,mode:'proxy',relay:'relay-stress.jsonl',mixedOperations:true},
];
// Subsequent isolated stages are registered only after confirming their identity.
if(fs.existsSync(path.join(root,'additional-stages.json')))actual.push(...json('additional-stages.json'));
const names=new Set(['request_started','request_finished','cache_task_attached','download_started','download_first_byte','download_progress','download_verified','download_published','cache_task_finished','cache_task_preempted','cache_task_canceled','cache_capacity_rejected']);
const allowed=['request_id','method','range','user_agent','resource_key','key','flight_id','song_id','status','bytes','size','outcome','cache','content_range','content_length','elapsed_ms','response_headers_ms','first_body_ms','stage','bytes_per_second','priority','waiting_priority'];
const round=n=>Math.round(n*1000)/1000;
const earliest=Math.min(...actual.map(stage=>Date.parse(app.find(e=>e.service_id===stage.service&&e.msg==='request_started')?.time)).filter(Number.isFinite));
if(!Number.isFinite(earliest))throw Error('No valid game requests');
const stages=actual.map(stage=>{
 const rows=app.filter(e=>e.service_id===stage.service&&names.has(e.msg));
 const first=rows.find(e=>e.msg==='request_started');
 if(!first)throw Error(`Missing ${stage.id}`);
 const start=Date.parse(first.time);
 const key=first.resource_key;
 const relevant=rows.filter(e=>(e.resource_key||e.key)===key);
 const reqs=new Map();
 for(const e of relevant){
  if(e.msg!=='request_started'&&e.msg!=='request_finished')continue;
  const r=reqs.get(e.request_id)||{id:e.request_id,method:e.method,range:e.range,role:e.user_agent?.startsWith('NSPlayer/')?'WMF-player':'resolver-user-agent',userAgent:e.user_agent};
  if(e.msg==='request_started')r.startMs=Date.parse(e.time)-start;
  else Object.assign(r,{endMs:Date.parse(e.time)-start,status:e.status,bytes:e.bytes,outcome:e.outcome,cache:e.cache,contentRange:e.content_range,firstBodyMs:e.first_body_ms,elapsedMs:e.elapsed_ms});
  reqs.set(e.request_id,r);
 }
 const requests=[...reqs.values()].sort((a,b)=>a.startMs-b.startMs);
 const intervals=requests.filter(r=>r.method==='GET'&&Number.isFinite(r.startMs)).map(r=>[r.startMs,r.endMs??null]);
 const gaps=[]; let end;
 for(const [a,b]of intervals){ if(end!==undefined&&a>end)gaps.push({fromMs:end,toMs:a,durationMs:a-end}); if(b===null){end=Infinity;break} end=Math.max(end??b,b) }
 const published=relevant.find(e=>e.msg==='download_published');
 const verified=relevant.find(e=>e.msg==='download_verified');
 const begun=relevant.find(e=>e.msg==='download_started');
 const lastGETEnd=Math.max(...requests.map(r=>Number.isFinite(r.endMs)?start+r.endMs:start));
 const meterEnd=published?Math.min(Date.parse(published.time),lastGETEnd):lastGETEnd;
 const samples=stage.relay?readLines(path.join(root,stage.relay)).flatMap(line=>{try{return[JSON.parse(line)]}catch{return[]}}).filter(e=>e.msg==='sample'&&Date.parse(e.time)>=start&&Date.parse(e.time)<=meterEnd).map(e=>({t:round((Date.parse(e.time)-start)/1000),bytes:e.limited_bytes,activeTCP:e.active})):[];
 const rates=samples.slice(1).map((s,i)=>(s.bytes-samples[i].bytes)/(s.t-samples[i].t)/1048576);
 const sorted=[...rates].sort((a,b)=>a-b);
 const median=sorted.length?sorted[Math.floor(sorted.length/2)]:null;
 const evidence=relevant.map(e=>{
  const item={t:round((Date.parse(e.time)-start)/1000),event:e.msg,source:e.source,line:e.line};
  for(const field of allowed)if(e[field]!==undefined)item[field]=e[field];
  return item;
 });
 return {id:stage.id,song:stage.song,rateBytesPerSecond:stage.rate,actualMode:stage.mode,initialResolverContaminated:!!stage.initialResolverContaminated,mixedOperations:!!stage.mixedOperations,key,fileBytes:begun?.size,offsetFromBatchSeconds:round((start-earliest)/1000),requests,internalGETGaps:gaps,verifiedAtSeconds:verified?round((Date.parse(verified.time)-start)/1000):null,publishedAtSeconds:published?round((Date.parse(published.time)-start)/1000):null,preemptions:relevant.filter(e=>e.msg==='cache_task_preempted').length,relay:{meter:'upstream TCP bytes including TLS, excluding API/catalog hosts; monitor traffic may share the meter',sampleMedianMiBps:median===null?null:round(median),samplePeakMiBps:rates.length?round(Math.max(...rates)):null,samples},evidence};
});
const preflight=json('preflight.json');
const gameFile=path.join(root,'game.raw.log');
// The raw snapshot must be saved by the operator workflow; do not read arbitrary paths here.
const game=readLines(gameFile).flatMap((line,i)=>{
 const stamp=line.match(/^(\d{4})\.(\d{2})\.(\d{2}) (\d{2}:\d{2}:\d{2})/);
 if(!stamp)return[];
 const t=Date.parse(`${stamp[1]}-${stamp[2]}-${stamp[3]}T${stamp[4]}+08:00`);
 if(t<earliest-10000)return[];
 let event;
 if(line.includes('[Video Playback] Attempting to resolve'))event='resolver_requested';
 else if(line.includes('[Video Playback]')&&line.includes(' resolved to '))event='resolver_returned';
 else if(line.includes('[Video Playback] ERROR:'))event='resolver_error';
 else if(line.includes('NativeProcess.Start:')&&line.includes('yt-dlp.exe'))event='yt_dlp_started';
 else if(line.includes('NativeProcess.HasExited:')&&line.includes('yt-dlp.exe'))event='yt_dlp_exited';
 else event=line.match(/\b(OnVideoStart|OnVideoReady|OnVideoEnd|OnVideoError|StopVideo)\b/)?.[1];
 if(!event)return[];
 const r={source:'game-session',line:i+1,t:round((t-earliest)/1000),event};
 const id=line.match(/[?&]id=(\d+)/)?.[1]; if(id)r.song=Number(id);
 if(event==='yt_dlp_exited'){r.exitCode=Number(line.match(/exited with code (-?\d+)/)?.[1]);r.durationMs=Number(line.match(/took (\d+) ms/)?.[1])}
 return[r];
});
const interrupted=app.filter(e=>e.service_id===actual[0].service&&e.resource_key==='303ee3623afb3fcf8b062f174b9ff4e6'&&['request_started','request_finished','cache_task_finished'].includes(e.msg)).map(e=>({t:round((Date.parse(e.time)-earliest)/1000),event:e.msg,status:e.status,bytes:e.bytes,outcome:e.outcome,source:e.source,line:e.line}));
const checks=[];
for(const stage of stages){
 const folder=stage.id==='baseline'?'storage':'storage-'+stage.id;
 const file=path.join(root,'data',folder,'videos',stage.key+'.mp4');
 if(fs.existsSync(file)){const data=fs.readFileSync(file);const md5=crypto.createHash('md5').update(data).digest('hex');checks.push({stage:stage.id,song:stage.song,size:data.length,md5,expectedMD5:stage.key,match:md5===stage.key&&data.length===stage.fileBytes});}
 else checks.push({stage:stage.id,song:stage.song,publishedFileExists:false});
}
fs.mkdirSync(destination,{recursive:true});
const write=(file,data)=>fs.writeFileSync(path.join(destination,file),JSON.stringify(data,null,2)+'\n');
write('observations.json',{schemaVersion:1,batch:'LIVE-20261014-01',scope:'actual VRChat Windows WMF player; stage boundaries and mixed operations must be retained; queue prefetch off',timeBasis:'each stage starts at its first validated HTTP request; game events use earliest valid baseline request as shared zero; game timestamps have one-second resolution',sourceSHA:json('session.json').sourceSHA,VRChatBuild:preflight.VRChatBuild,WindowsBuild:preflight.WindowsBuild,stages,gameEvents:game,excludedRestartInterference:interrupted,fileChecks:checks});
console.log(JSON.stringify(stages.map(({id,song,fileBytes,requests,internalGETGaps,publishedAtSeconds,relay})=>({id,song,fileBytes,requests,gaps:internalGETGaps,publishedAtSeconds,medianMiBps:relay.sampleMedianMiBps,peakMiBps:relay.samplePeakMiBps})),null,2));
