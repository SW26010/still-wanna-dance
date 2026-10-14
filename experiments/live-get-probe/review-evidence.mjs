// Commit concise evidence only. The detailed allowlisted trace remains local.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';

const root=path.resolve(process.argv[2]||'artifacts/acceptance-evidence/LIVE-20261014-01');
const out=path.resolve('docs/evidence/live-20261014-01');
const read=name=>JSON.parse(fs.readFileSync(path.join(root,name),'utf8').replace(/^\uFEFF/,''));
const all=read('sanitized-full/observations.json');
const session=read('session.json');
const round=n=>Math.round(n*1000)/1000;
const stress=all.stages.find(s=>s.id==='stress');
const baselineTime=Date.parse('2026-10-14T06:35:49.1347075Z');
const naturalEndMs=Date.parse(read('operation-seek-forward.json').requestedUtc)-baselineTime;
const compact=s=>({id:s.id,song:s.song,rateBytesPerSecond:s.rateBytesPerSecond,actualMode:s.actualMode,key:s.key,fileBytes:s.fileBytes,offsetFromBatchSeconds:s.offsetFromBatchSeconds,initialResolverContaminated:s.initialResolverContaminated,mixedOperations:s.mixedOperations,GETCount:s.requests.length,playerGETCount:s.requests.filter(r=>r.role==='WMF-player').length,completedPlayerGETCount:s.requests.filter(r=>r.role==='WMF-player'&&r.outcome==='completed').length,maximumInternalGETGapMs:Math.max(0,...s.internalGETGaps.map(g=>g.durationMs)),publishedAtSeconds:s.publishedAtSeconds,preemptions:s.preemptions,relayMedianMiBps:s.relay.sampleMedianMiBps,relayPeakMiBps:s.relay.samplePeakMiBps,requestExamples:s.requests.length<=2?s.requests:s.requests.filter(r=>s.id==='stress'?[1,2,3,16,21,22].includes(r.id):[s.requests[0].id,s.requests.at(-1).id].includes(r.id))});
const progress=stress.evidence.filter(e=>e.event==='download_progress'&&e.t>=225&&e.t<=260).map(e=>({t:e.t,bytes:e.bytes,source:e.source,line:e.line}));
const publicData={schemaVersion:2,batch:all.batch,status:'in-progress',sourceSHA:all.sourceSHA,VRChatBuild:all.VRChatBuild,WindowsBuild:all.WindowsBuild,timeBasis:all.timeBasis,scope:'GET behavior only; independent cold-cache natural-play samples, plus explicitly separated exploratory operations. No actual preemption acceptance.',stages:all.stages.map(compact),natural128KiB:{song:6776,windowEndSeconds:round(naturalEndMs/1000),endReason:'assistant requested first seek; later interactions excluded from this natural-play window',feedback:read('feedback-stress-natural.json').feedback,completedPlayerRequests:stress.requests.filter(r=>r.role==='WMF-player'&&r.endMs<=naturalEndMs).length,maximumReconnectGapAfterResolverMs:Math.max(0,...stress.internalGETGaps.filter(g=>g.fromMs>10000&&g.toMs<=naturalEndMs).map(g=>g.durationMs)),finalRequestCensored:true},exploratorySeek128KiB:{classification:'mixed operations, not independent Seek acceptance',forwardFeedback:read('feedback-seek-forward.json').feedback,backwardFeedback:read('feedback-seek-backward.json').feedback,allGETsIdleGap:stress.internalGETGaps.find(g=>g.durationMs>10000),progressAroundGap:progress,note:'Merge all overlapping requests before calculating idle time. The finite request ended earlier, but another old GET remained active. P0 release inferred from source; no direct demand-release instrumentation.'},fileChecks:all.fileChecks,gameEventCounts:all.gameEvents.reduce((a,e)=>(a[e.event]=(a[e.event]||0)+1,a),{}),excludedSamples:[{id:'2m-initial-resolver',reason:'Song 2502 began during gateway restart; previous service returned canceled 503. Subsequent WMF transfer retained with limitation.'},{id:'quarter',reason:read('excluded-quarter.json').reason,songs:[6776,10689,2821],feedback:read('feedback-quarter-excluded.json').feedback}],pending:['Independent 256 KiB/s natural play','Independent 64 KiB/s natural play','Isolated Seek, refresh and node-switch acceptance deferred by user'],limitations:['Different songs and times; not a same-content throughput A/B.','Aggregate upstream TCP cap includes TLS and any monitor traffic; not gateway-to-player throttle.','No queue prefetch in this round. Canceled songs can continue background downloads until gateway reset.','Detailed sanitized trace is local and unpublished; Git retains concise evidence only.'],cleanup:{status:'test still active',previousCleanup:'The first four cases were cleaned up and hosts verified byte-identical, then testing resumed at user request.'}};
// Finalize only from verified cleanup and explicit user closure, preserving unknown modes.
const finalStop=read('feedback-final-stop.json');
const cleanup=read('cleanup.json');
if(!cleanup.hostsExactMatch||cleanup.managedAPI||cleanup.remainingListeners.length||cleanup.remainingTestProcesses.length||cleanup.temporaryFiles.length)throw Error('Cleanup incomplete');
publicData.status='closed-partial-acceptance';
publicData.playerModes={formal:'Must enqueue first; no seek control; human enqueue-to-play delay is separate from load latency',preview:'Immediate, starts at a default nonzero position, seekable; can coexist with formal playback',historical:'First four modes not confirmed; user cannot recall 6776 mode',source:'Explicit user clarification in this live-test conversation'};
for(const s of publicData.stages){
 const full=all.stages.find(x=>x.id===s.id);
 s.playerMode=['quarter-clean','64k'].includes(s.id)?'formal':'unknown';
 s.modeEvidence=s.id==='quarter-clean'?'User explicitly confirmed 1981 formal':s.id==='64k'?'Formal-only operation requested; game start confirms song 1344; user reported playback':'Not retrospectively assumed';
 const stop=all.gameEvents.find(e=>e.event==='StopVideo'&&e.t>=s.offsetFromBatchSeconds&&e.t<s.offsetFromBatchSeconds+300);
 if(['quarter-clean','64k'].includes(s.id)){
  s.gameStopAtSeconds=stop?round(stop.t-s.offsetFromBatchSeconds):null;
  s.gameStopSource=stop?{source:stop.source,line:stop.line}:null;
  const wmfs=full.requests.filter(r=>r.role==='WMF-player');
  const cutoff=s.gameStopAtSeconds*1000;
  const gaps=full.internalGETGaps.filter(g=>g.fromMs>=wmfs[0].startMs&&g.toMs<cutoff);
  s.playingReconnectGapsMs=gaps.map(g=>g.durationMs);
  s.playerGETsStartedBeforeStop=wmfs.filter(r=>r.startMs<cutoff).length;
  s.playerGETsStartedAfterStop=wmfs.filter(r=>r.startMs>=cutoff).length;
  s.lastGETEndedAfterGameStopSeconds=round(Math.max(...wmfs.map(r=>r.endMs))/1000-s.gameStopAtSeconds);
  s.feedback=read('feedback-'+s.id+'.json').feedback;
  s.requestExamples=full.requests.filter(r=>s.id==='quarter-clean'?[1,2,3].includes(r.id):[1,2,3,4,5,6,8].includes(r.id));
  s.gameEvents=all.gameEvents.filter(e=>e.t>=s.offsetFromBatchSeconds-3&&e.t<=s.offsetFromBatchSeconds+s.gameStopAtSeconds+1).filter(e=>e.song===s.song||e.event==='StopVideo'||e.event==='yt_dlp_exited').map(e=>({event:e.event,t:round(e.t-s.offsetFromBatchSeconds),source:e.source,line:e.line,song:e.song,exitCode:e.exitCode,durationMs:e.durationMs}));
 }
 const samples=full.relay.samples;
 if(samples.length>1)s.relayMeanMiBps=round((samples.at(-1).bytes-samples[0].bytes)/(samples.at(-1).t-samples[0].t)/1048576);
 s.requestExamples=s.requestExamples.map(({id,role,range,startMs,endMs,bytes,status,outcome,firstBodyMs})=>({id,role,range,startMs,endMs,bytes,status,outcome,firstBodyMs}));
}
publicData.natural128KiB.playerMode='unknown';
publicData.natural128KiB.interpretation='HTTP changes and reported stutter are observed; cannot retrospectively attribute every nonzero range to automatic reconnect by one player because preview has a nonzero default offset';
publicData.exploratorySeek128KiB.playerMode='seek requires preview; coexistence with formal playback not confirmed';
publicData.exploratorySeek128KiB.note+=' This is a same-resource all-GET gap; not evidence of an 18-second formal-player-only gap.';
publicData.pending=['Isolated preview Seek, refresh and node switching not executed; user explicitly chose closure','Actual cross-resource task preemption during a GET gap not reproduced; LA-008/037 remain open'];
publicData.limitations.push('Game StopVideo times have one-second resolution. GETs that start after StopVideo are excluded from playing-reconnect statistics.','Neither partial low-bandwidth file was published; absence of whole-file MD5 is intentional, not a claimed pass.');
publicData.cleanup={status:'verified',hostsByteIdentical:true,managedMappingAbsent:true,testProcessesRemaining:0,testListenersRemaining:0,temporaryFilesRemaining:0,userClosure:finalStop.feedback,retained:'Private original logs, isolated config/cache, frozen binaries and detailed sanitized trace remain in ignored local lab; no game process terminated'};
fs.mkdirSync(out,{recursive:true});
const write=(name,value)=>fs.writeFileSync(path.join(out,name),JSON.stringify(value,null,2)+'\n');
write('observations.json',publicData);
const files=fs.readdirSync(out).filter(name=>name.endsWith('.json')&&name!=='manifest.json').sort().map(name=>{const b=fs.readFileSync(path.join(out,name));return {path:name,bytes:b.length,sha256:crypto.createHash('sha256').update(b).digest('hex'),source:'Allowlisted app-log events, relay samples, game-event tokens, local file hashes, explicit user feedback and verification receipts',redaction:'Relative times; no usernames, room IDs, raw URLs, tokens, terms credentials, absolute private paths or raw game lines'};});
const checksPath=path.join(out,'checks.json');
const verifiedChecks=fs.existsSync(checksPath)?JSON.parse(fs.readFileSync(checksPath,'utf8')):undefined;
const toolFiles=['main.go','main_test.go','observe.mjs','summarize.mjs','review-evidence.mjs','README.md'].map(name=>{const file='experiments/live-get-probe/'+name;const b=Buffer.from(fs.readFileSync(file,'utf8').replace(/\r\n/g,'\n'));return {path:file,bytes:b.length,sha256:crypto.createHash('sha256').update(b).digest('hex')};});
write('manifest.json',{schemaVersion:2,batch:all.batch,status:'closed-partial-acceptance',hashBasis:'git-blob',lineEndings:'LF',sourceSHA:session.sourceSHA,executables:[{name:'SWD.exe',sha256:session.consoleSHA256},{name:'get-probe.exe',sha256:session.relaySHA256}],build:{go:session.goVersion,tags:session.buildTags,CGO:session.buildCGO,package:null},files,testedCodePatch:verifiedChecks?.testedCodePatch,tools:toolFiles,fullTrace:{status:'local-unpublished',note:'No public attachment or archive URL claimed. Git contains concise evidence supporting the report; raw private logs are not publishable.'}});
console.log(JSON.stringify({stages:publicData.stages.map(s=>s.id),status:publicData.status,files:files.map(f=>({path:f.path,bytes:f.bytes}))}));
