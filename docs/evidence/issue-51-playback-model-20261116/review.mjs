import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
const read=p=>JSON.parse(fs.readFileSync(p,'utf8').replace(/^\uFEFF/,''));
const round=x=>Math.round(x*1000)/1000;
const sources=[],cases=[];
function source(p){if(!sources.some(s=>s.path===p))sources.push({path:p,sha256:crypto.createHash('sha256').update(fs.readFileSync(p,'utf8').replace(/\r\n/g,'\n')).digest('hex')});return read(p);}
function add(id,p,requests,game=[]){
 requests.sort((a,b)=>(a.t??-Infinity)-(b.t??-Infinity));
 const first=requests.find(r=>r.player), pairs=[], overlaps=[];
 for(let i=0;i<requests.length;i++){
  const r=requests[i],prev=requests[i-1];
  if(r.player&&prev&&!prev.player&&prev.range==='')pairs.push({resolver:prev.id,player:r.id,t:r.t,range:r.range,startsAtZeroOrUnranged:r.range===''||/^bytes=0-(?:\d+)?$/.test(r.range),firstPlayer:r===first,resolverStartGapSeconds:prev.t===null||r.t===null?null:round(r.t-prev.t),resolverEndGapSeconds:prev.end===null||prev.end===undefined||r.t===null?null:round(r.t-prev.end)});
  if(r.player&&r.t!==null)for(const old of requests.slice(0,i).filter(x=>x.player&&x.end!=null&&x.end>r.t))overlaps.push({older:old.id,newer:r.id,secondsUntilOldEnded:round(old.end-r.t)});
 }
 cases.push({id,source:p,firstPlayer:first?{id:first.id,t:first.t,range:first.range}:null,resolverPlayerPairs:pairs,playerOverlaps:overlaps,requests,gameAuditOnly:game});
}
let p='docs/evidence/live-20261116-01/engine-playback.json';
for(const c of source(p).samples)add('01/'+c.id,p,c.requests.map(r=>({id:r.id,t:r.t,player:r.player,range:r.range,end:r.finish?.t??null,bytes:r.finish?.bytes??null,status:r.finish?.status??null})),(c.gameEvents??c.game??[]).filter(g=>/OnVideoStart|Retrying|OnVideoError/.test(g.event)).map(({t,event,owner})=>({t,event,owner})));
p='docs/evidence/live-20261116-02/playback.json';
for(const c of source(p).cases)add('02/'+c.id,p,c.requests.map(r=>({id:r.id,t:r.t,player:r.player,range:r.range,end:r.finish?.t??null,bytes:r.finish?.bytes??null,status:r.finish?.status??null})),c.gameEvents.filter(g=>/OnVideoStart|Retrying|OnVideoError|Reload/.test(g.event)).map(({t,event})=>({t,event})));
p='docs/evidence/live-20261116-03/playback.json';
for(const c of source(p).cases)add('03/'+c.song,p,c.http.filter(r=>r.event==='request').map(r=>{const end=c.http.find(e=>e.id===r.id&&e.event==='response-ended');return {id:r.id,t:r.t,player:r.player,range:r.range,end:end?.t??null,bytes:end?.bytes??null};}),c.game);
for(const day of ['04','05','06','08']){
 p=`docs/evidence/live-20261113-${day}/${day==='04'?'formal-retries':'midbody'}.json`;
 const c=source(p);add('1113/'+day,p,c.requestTable.filter(r=>day!=='06'||r.request>=5).map(r=>({id:r.request,t:r.startMs/1000,player:r.client==='WMF',range:r.range,end:r.endMs==null?null:r.endMs/1000,bytes:r.bytes,status:r.status})),[]);
}
p='docs/evidence/live-20261028-01/cold-seek.json';
const seek=source(p);
const seekStarts=seek.requests.filter(r=>r.event==='request_started').map(r=>({id:r.request,t:r.atMs/1000,range:r.range}));
const summary={caseCount:cases.length,pairs:cases.reduce((n,c)=>n+c.resolverPlayerPairs.length,0),pairsAfterFirstPlayer:cases.reduce((n,c)=>n+c.resolverPlayerPairs.filter(p=>!p.firstPlayer).length,0),firstPlayerWithoutRange:cases.filter(c=>c.firstPlayer?.range==='').map(c=>c.id),firstPlayerNonzeroRange:cases.filter(c=>c.firstPlayer?.range&&!c.firstPlayer.range.startsWith('bytes=0-')).map(c=>c.id),casesWithPlayerOverlap:cases.filter(c=>c.playerOverlaps.length).map(c=>c.id)};
const gaps=cases.flatMap(c=>c.resolverPlayerPairs).filter(p=>Number.isFinite(p.resolverEndGapSeconds));
summary.timedPairs=gaps.length;summary.resolverEndGapSeconds={min:Math.min(...gaps.map(p=>p.resolverEndGapSeconds)),max:Math.max(...gaps.map(p=>p.resolverEndGapSeconds))};
const result={method:'Read-only replay of selected recorded HTTP sequences; game events are independent audit labels, not candidate runtime inputs. Adjacent resolver/player means adjacent request starts, not proof of a new play. Null times remain unknown. Counts describe this selected dataset, not frequency in normal use.',limitations:['1113/06 requests1-4 belong to preceding media and are excluded.','01/N10R and02/manual4598 retain original timing/cache limitations; first observed request is not proof of first request in playback lifetime.','Player/resolver labels are inherited from source evidence; User-Agent distinguishes observed client families, not unique users.','No inference of delivered video bytes from error-response bodies.'],sources,summary,cases,coldSeek:{source:p,rangeStart:seek.rangeStart,targetBlock:seek.targetBlock,requests:seekStarts}};
const out='docs/evidence/issue-51-playback-model-20261116';fs.mkdirSync(out,{recursive:true});fs.writeFileSync(path.join(out,'request-features.json'),JSON.stringify(result,null,2)+'\n');
console.log(JSON.stringify(summary));
for(const id of ['01/H2','02/manual4598','02/manual8935','1113/05','1113/08']){const c=cases.find(c=>c.id===id);console.log(JSON.stringify({id,first:c.firstPlayer,pairs:c.resolverPlayerPairs,overlaps:c.playerOverlaps}));}
console.log(JSON.stringify({coldSeek:seekStarts.filter(r=>r.t>=0).slice(0,4)}));
