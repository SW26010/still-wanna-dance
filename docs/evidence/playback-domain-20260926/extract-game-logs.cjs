const fs=require('node:fs');
const path=require('node:path');
const crypto=require('node:crypto');
const root='<log-collector-directory>/logs/source-vrc-logs';
const files=fs.readdirSync(root).filter(x=>/^output_log_.*\.txt$/.test(x)).sort();
const domains={},transitions={},manifest=[];
function safeURL(raw){const u=new URL(raw);if(!/^(api\.udon\.dance|play\.udon\.dance|nya\.xin\.moe)$/.test(u.hostname)){u.search='';u.hash='';u.username='';u.password='';}return u.href;}
let udPlayMentions=0,udNyaMentions=0,playbackLines=0;
for(const file of files){
  const data=fs.readFileSync(path.join(root,file));
  manifest.push({file,bytes:data.length,sha256:crypto.createHash('sha256').update(data).digest('hex')});
  for(const [index,line] of data.toString('utf8').split(/\r?\n/).entries()){
    udPlayMentions+=(line.match(/ud-play\.kiva\.moe/gi)||[]).length;
    udNyaMentions+=(line.match(/ud-nya\.kiva\.moe/gi)||[]).length;
    const playback=/LoadRoutedURL:|\[Video Playback\]/.test(line);
    const urls=[...line.matchAll(/https?:\/\/[^\s"'<>\\]+/g)].map(m=>m[0]);
    if(playback) playbackLines++;
    for(const raw of urls){
      let u;try{u=new URL(raw);}catch{continue;}
      if(!playback&&!/^(api\.udon\.dance|play\.udon\.dance|nya\.xin\.moe|aya\.kiva\.moe|ud-play\.kiva\.moe|ud-nya\.kiva\.moe)$/.test(u.hostname))continue;
      const d=domains[u.host]??={allRelevantURLMentions:0,playbackURLMentions:0,files:new Set(),first:{file,line:index+1},last:null,examples:[]};
      d.allRelevantURLMentions++;d.files.add(file);d.last={file,line:index+1};
      if(playback){d.playbackURLMentions++;if(d.examples.length<3)d.examples.push({file,line:index+1,event:line.includes('LoadRoutedURL:')?'LoadRoutedURL':'Video Playback',url:safeURL(raw)});}
    }
    if(playback&&urls.length>=2&&/resolved to|routed to/.test(line)){
      let key;try{key=new URL(urls[0]).host+' -> '+new URL(urls[1]).host;}catch{continue;}
      const t=transitions[key]??={count:0,examples:[]};t.count++;
      if(t.examples.length<2)t.examples.push({file,line:index+1,from:safeURL(urls[0]),to:safeURL(urls[1])});
    }
  }
}
for(const d of Object.values(domains))d.files=[...d.files];
const result={createdAt:new Date().toISOString(),root,files:manifest,collectionContext:'User states most logs were collected while <reference-workspace>/wanna-cdn.exe was running. These are client-visible URLs potentially influenced by local CDN caching/redirects; not clean origin traces. No per-session CDN state is established.',scope:'Domain URL mentions in LoadRoutedURL / Video Playback lines, plus named dance hosts elsewhere. Examples retain only URLs and line numbers, not player names or full logs. Query parameters are removed from other sites. Counts are mentions, not requests or successful plays.',playbackLines,udPlayMentions,udNyaMentions,domains,transitions};
fs.writeFileSync(path.join(__dirname,'game-log-domains.json'),JSON.stringify(result,null,2));
console.log(JSON.stringify({fileCount:files.length,first:files[0],last:files.at(-1),udPlayMentions,udNyaMentions,domains:Object.fromEntries(Object.entries(domains).map(([h,d])=>[h,{playback:d.playbackURLMentions,all:d.allRelevantURLMentions,files:d.files.length}])),transitions:Object.fromEntries(Object.entries(transitions).filter(([key])=>key.includes('udon')||key.includes('nya.xin')||key.includes('139.196')).map(([key,t])=>[key,t]))},null,2));
