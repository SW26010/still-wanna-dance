// Compact evidence only; detailed request traces remain in the ignored lab.
import fs from 'node:fs';
import path from 'node:path';
const root=path.resolve(process.argv[2]);
const output=path.resolve(process.argv[3]);
const read=f=>JSON.parse(fs.readFileSync(f,'utf8').replace(/^\uFEFF/,''));
const rounded=n=>Math.round(n*1000)/1000;
const gapsFor=requests=>{
  let covered=-Infinity;const gaps=[];
  for(const r of requests.filter(r=>r.start!==undefined).sort((a,b)=>a.start-b.start)){
    if(covered!==-Infinity&&r.start>covered)gaps.push({from:covered,to:r.start,seconds:rounded(r.start-covered)});
    covered=Math.max(covered,r.end??Infinity);
  }
  return gaps;
};
const stages=fs.readdirSync(path.join(root,'sanitized-full')).filter(n=>n.endsWith('.json')).map(n=>{
  const s=read(path.join(root,'sanitized-full',n));
  const meta=read(path.join(root,'stage-'+s.stage+'.json'));
  const req=s.requests;
  let end=-Infinity,maxActive=0;
  const gaps=[];
  for(const r of req.filter(r=>r.start!==undefined).sort((a,b)=>a.start-b.start)){
    if(end!==-Infinity&&r.start>end)gaps.push({from:end,to:r.start,seconds:rounded(r.start-end)});
    end=Math.max(end,r.end??Infinity);
  }
  const times=req.flatMap(r=>r.start===undefined?[]:[[r.start,1],...(r.end===undefined?[]:[[r.end,-1]])]).sort((a,b)=>a[0]-b[0]||a[1]-b[1]);
  let active=0;for(const [,delta]of times){active+=delta;maxActive=Math.max(maxActive,active);}
  const keyEvents=s.events.filter(e=>['download_started','download_verified','download_published','cache_task_finished','cache_task_preempted','playback_local_hit'].includes(e.event));
  const resourceKeys=[...new Set(req.map(r=>r.key).filter(Boolean))];
  const resources=resourceKeys.map(key=>{
    const requests=req.filter(r=>r.key===key);
    const events=s.events.filter(e=>(e.resource_key||e.key)===key);
    const published=events.find(e=>e.event==='download_published');
    return {key,requestCount:requests.length,internalGETGaps:gapsFor(requests),
      downloadStartedAt:events.find(e=>e.event==='download_started')?.t,
      publishedAt:published?.t,bytes:published?.size||events.find(e=>e.event==='download_started')?.size,
      HITResponses:requests.filter(r=>r.cache==='HIT').length,
      flightIDs:[...new Set(events.filter(e=>e.event==='cache_task_attached').map(e=>e.flight_id))]};
  });
  const selected=new Set([0,1,2,3,req.length-1]);
  for(const r of req)if(r.status===304||r.status>=400)selected.add(req.indexOf(r));
  for(const g of s.gameEvents.filter(g=>g.event==='resolver_requested')){
    const i=req.findIndex(r=>r.start>=g.t-1);
    if(i>=0)for(let k=Math.max(0,i-1);k<Math.min(req.length,i+3);k++)selected.add(k);
  }
  for(const op of s.operations.filter(op=>op.event==='instruction'&&op.action?.startsWith('seek-'))){
    const i=req.findIndex(r=>r.start>=op.t);
    if(i>=0)for(let k=Math.max(0,i-1);k<Math.min(req.length,i+3);k++)selected.add(k);
  }
  const summary={id:s.stage,rateBytesPerSecond:s.rateBytesPerSecond,timeBasis:s.timeBasis,
    requestCount:req.length,WMFRequestCount:req.filter(r=>r.role==='WMF-player').length,
    openRequests:req.filter(r=>r.end===undefined).length,maximumConcurrentGETs:maxActive,
    internalAllGETGaps:gaps,resourceKeys,resources,
    attachedFlightIDs:[...new Set(s.events.filter(e=>e.event==='cache_task_attached').map(e=>e.flight_id))],
    requestExamples:req.filter((_,i)=>selected.has(i)),keyEvents,
    operations:s.operations,gameEvents:s.gameEvents,
    limits:'GET concurrency is downstream and does not measure upstream request budget. Open requests are right-censored. Operation timestamps are instruction/acknowledgment times. Game callback is not proof of visible playback.'};
  return {order:Date.parse(meta.startedUtc),summary};
}).sort((a,b)=>a.order-b.order).map(x=>x.summary);
fs.mkdirSync(path.dirname(output),{recursive:true});
const cleanupPath=path.join(root,'cleanup.json');
const cleanup=fs.existsSync(cleanupPath)?read(cleanupPath):null;
const closed=cleanup?.restore?.exactMatch===true&&cleanup.labProcesses.length===0&&cleanup.listeners.length===0&&cleanup.temporaryFiles===0;
const cacheChecks=fs.readdirSync(root).filter(n=>/^cache-\d+\.json$/.test(n)).map(n=>{
  const c=read(path.join(root,n));const {checkedUtc,...safe}=c;return safe;
});
fs.writeFileSync(output,JSON.stringify({schemaVersion:1,batch:path.basename(root),status:closed?'closed-partial-acceptance':'in-progress',sourceSHA:'3fae4b81642f27a7a406a828bd8fa0eb79dc8e94',cacheChecks,stages},null,2)+'\n');
console.log(JSON.stringify({stages:stages.map(s=>s.id),bytes:fs.statSync(output).size}));
