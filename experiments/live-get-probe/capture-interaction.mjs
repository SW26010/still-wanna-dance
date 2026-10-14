// Batch-generic, read-only capture of one explicitly operated interaction case.
// Detailed allowlisted traces remain in the ignored lab; review before publication.
import fs from 'node:fs';
import path from 'node:path';
const root=path.resolve(process.argv[2]);
const stage=process.argv[3];
if(!stage||!/^[a-z0-9-]+$/.test(stage))throw Error('Stage required');
const json=f=>JSON.parse(fs.readFileSync(path.join(root,f),'utf8').replace(/^\uFEFF/,''));
const lines=f=>fs.existsSync(f)?fs.readFileSync(f,'utf8').split(/\r?\n/):[];
const read=f=>lines(f).flatMap((l,i)=>{try{return [{...JSON.parse(l),source:path.basename(f),line:i+1}]}catch{return []}});
const meta=json('stage-'+stage+'.json');
const logs=fs.readdirSync(path.join(root,'data/logs')).filter(n=>/^app\.log(?:\.\d+)?$/.test(n)).flatMap(n=>read(path.join(root,'data/logs',n))).sort((a,b)=>Date.parse(a.time)-Date.parse(b.time));
const ready=logs.find(e=>e.msg==='cache_engine_ready'&&e.service_id===meta.service);
if(!ready)throw Error('Stage service identity missing');
const zero=Date.parse(ready.time);
const next=logs.find(e=>e.msg==='cache_engine_ready'&&Date.parse(e.time)>zero&&e.service_id!==meta.service);
const end=next?Date.parse(next.time):Date.now();
const t=time=>Math.round((Date.parse(time)-zero)*1000)/1000000;
const allowed=['request_id','resource_key','key','flight_id','background','range','method','status','bytes','size','expected_bytes','outcome','cache','content_range','elapsed_ms','first_body_ms','stage','priority','waiting_priority'];
const hosts=new Map();
const events=logs.filter(e=>e.service_id===meta.service&&/^(request_started|request_finished|cache_task_attached|cache_task_finished|cache_task_preempted|cache_task_canceled|download_started|download_first_byte|download_progress|download_verified|download_published|playback_local_hit|playback_song_failed)$/.test(e.msg)).map(e=>{const r={t:t(e.time),event:e.msg,source:e.source,line:e.line};for(const f of allowed)if(e[f]!==undefined)r[f]=e[f];if(e.host){if(!hosts.has(e.host))hosts.set(e.host,'host-'+(hosts.size+1));r.hostAlias=hosts.get(e.host);}if(e.error)r.errorClass=/context canceled/i.test(e.error)?'context-canceled':/deadline|timeout/i.test(e.error)?'timeout':'other-redacted';if(e.user_agent)r.role=e.user_agent.startsWith('NSPlayer/')?'WMF-player':e.user_agent.startsWith('SWD-Live02-Warmup')?'lab-warmup':'resolver-user-agent';return r;});
const requests=new Map();
for(const e of events){if(!['request_started','request_finished'].includes(e.event))continue;const r=requests.get(e.request_id)||{id:e.request_id,key:e.resource_key,role:e.role,range:e.range,hostAlias:e.hostAlias};if(e.event==='request_started')r.start=e.t;else Object.assign(r,{end:e.t,status:e.status,bytes:e.bytes,outcome:e.outcome,cache:e.cache,firstBodyMs:e.first_body_ms});requests.set(e.request_id,r);}
const operations=read(path.join(root,'operations.jsonl')).filter(e=>e.stage===stage).map(({time,source,line,...e})=>({...e,t:t(time)}));
const game=lines(path.join(root,'game.raw.log')).flatMap((l,i)=>{const m=l.match(/^(\d{4})\.(\d{2})\.(\d{2}) (\d{2}:\d{2}:\d{2})/);if(!m)return[];const time=`${m[1]}-${m[2]}-${m[3]}T${m[4]}+08:00`;if(Date.parse(time)<zero||Date.parse(time)>=end)return[];let event=l.match(/\b(OnVideoStart|OnVideoReady|OnVideoEnd|OnVideoError|StopVideo)\b/)?.[1];if(l.includes('[Video Playback] Attempting to resolve'))event='resolver_requested';else if(l.includes('[Video Playback]')&&l.includes(' resolved to '))event='resolver_returned';if(!event)return[];return [{t:t(time),event,source:'game-session',line:i+1,song:l.match(/[?&]id=(\d+)/)?.[1],node:l.match(/[?&]node=(cf|nya)\b/)?.[1]}];});
const output={batch:path.basename(root),stage,rateBytesPerSecond:meta.rateBytesPerSecond,timeBasis:'seconds from this service cache_engine_ready; game timestamps one-second resolution; operation instruction/feedback times are not exact action times',requests:[...requests.values()],operations,events,gameEvents:game};
fs.mkdirSync(path.join(root,'sanitized-full'),{recursive:true});
fs.writeFileSync(path.join(root,'sanitized-full',stage+'.json'),JSON.stringify(output,null,2)+'\n');
console.log(JSON.stringify({stage,requests:output.requests,latestDownloadEvents:events.filter(e=>e.event.startsWith('download_')).slice(-2),gameEvents:game.slice(-6)},null,2));
