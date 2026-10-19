// Read-only allowlisted summary; raw URLs and paths never enter the output.
import fs from 'node:fs';
const events=fs.readFileSync(process.argv[2],'utf8').split(/\r?\n/).filter(Boolean).map(l=>JSON.parse(l));
const zero=Date.parse(events.find(e=>e.msg==='body_probe_ready')?.time||events[0].time);
const relative=t=>Math.round((Date.parse(t)-zero)*1000)/1e6;
const isVideoResponse=r=>r.method==='GET'&&(r.status===200||r.status===206);
const requests=events.filter(e=>e.msg==='body_request').map(e=>{
 const own=events.filter(x=>x.request_id===e.request_id), h=own.find(x=>x.msg==='body_headers'),writes=own.filter(x=>x.msg==='body_write'),end=own.find(x=>x.msg==='body_finished');
 const videoWrites=isVideoResponse({method:e.method,status:h?.status})?writes:[];
 return {id:e.request_id,song:e.song,method:e.method,range:e.range,role:e.player?'WMF-player':'resolver-or-other',start:relative(e.time),status:h?.status,contentRange:h?.content_range,declaredLength:h?.content_length,writeBytes:writes.reduce((n,w)=>n+w.bytes,0),firstWrite: writes[0]&&relative(writes[0].time),lastWrite:writes.length&&relative(writes.at(-1).time),end:end&&relative(end.time),canceled:end?.canceled,writeFailures:writes.filter(w=>!w.write_ok||!w.flush_ok).length,intervals:merge(videoWrites.filter(w=>w.bytes>0).map(w=>[w.offset,w.offset+w.bytes]))};
});
function merge(ranges){const out=[];for(const r of ranges.sort((a,b)=>a[0]-b[0])){if(out.length&&out.at(-1)[1]>=r[0])out.at(-1)[1]=Math.max(out.at(-1)[1],r[1]);else out.push([...r]);}return out;}
const songs=[...new Set(requests.map(r=>r.song))].map(song=>{const player=requests.filter(r=>r.song===song&&r.role==='WMF-player'&&isVideoResponse(r));const intervals=merge(player.flatMap(r=>r.intervals));return {song,playerTotalBytes:player.reduce((n,r)=>n+r.writeBytes,0),playerUniqueBytes:intervals.reduce((n,r)=>n+r[1]-r[0],0),intervals}});
console.log(JSON.stringify({timeBasis:'seconds from body_probe_ready; successful server Write is not proof of application consumption',requests,songs,limits:events.filter(e=>e.msg==='body_limit').map(e=>({t:relative(e.time),limitBytes:e.limit_bytes,deliveredBytes:e.delivered_bytes}))},null,2));
