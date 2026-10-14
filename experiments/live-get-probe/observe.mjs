// Read-only live summary. Never emits raw game logs, URLs, query strings or tokens.
import fs from 'node:fs';
import path from 'node:path';

const root = path.resolve(process.argv[2] || 'artifacts/acceptance-evidence/LIVE-20261014-01');
const readJSONL = file => fs.existsSync(file) ? fs.readFileSync(file,'utf8').split(/\r?\n/).filter(Boolean).flatMap(x=>{try{return [JSON.parse(x)]}catch{return []}}) : [];
const logs = [3,2,1,0].flatMap(n=>readJSONL(path.join(root,'data/logs/app.log'+(n?'.'+n:''))));
const requests = new Map();
const resources = new Map();
const origins = new Map();
for(const e of logs) {
  const t=Date.parse(e.time);
  const sid=e.service_id||'unknown';
  if(e.msg==='cache_engine_ready') origins.set(sid,t);
  const key=e.resource_key||e.key;
  if(key) {
    const rk=sid+':'+key;
    const r=resources.get(rk)||{key,service:sid,events:[]};
    if(['download_started','download_first_byte','download_progress','download_verified','download_published','cache_task_finished','cache_task_preempted','cache_task_canceled'].includes(e.msg)) {
      r.events.push({t,event:e.msg,bytes:e.bytes,total:e.total,size:e.size,outcome:e.outcome,stage:e.stage});
    }
    resources.set(rk,r);
  }
  if(e.msg==='request_started'||e.msg==='request_finished') {
    const id=sid+':'+e.request_id;
    const r=requests.get(id)||{id:e.request_id,service:sid,method:e.method,range:e.range,ua:e.user_agent};
    if(key) r.key=key;
    if(e.msg==='request_started') r.start=t;
    else Object.assign(r,{end:t,arrival:t-e.elapsed_ms,status:e.status,bytes:e.bytes,outcome:e.outcome,cache:e.cache,contentRange:e.content_range,contentLength:e.content_length,firstBodyMs:e.first_body_ms});
    requests.set(id,r);
  }
}
const now=Date.now();
const relative=(t,sid)=>t===undefined?undefined:Math.round((t-(origins.get(sid)||t))*1000)/1000000;
const summary={
  note:'Times relative to each cache_engine_ready; open requests are right-censored. Starts follow URL validation, not exact addDemand execution.',
  requests:[...requests.values()].map(r=>({...r,start:relative(r.start,r.service),arrival:relative(r.arrival,r.service),end:relative(r.end,r.service),activeSeconds:r.end===undefined&&r.start!==undefined?Math.round((now-r.start)/1000):undefined})),
  resources:[...resources.values()].filter(r=>r.events.length).map(r=>({...r,events:r.events.map(e=>({...e,t:relative(e.t,r.service)}))}))
};
console.log(JSON.stringify(summary,null,2));
