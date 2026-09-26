const {request}=require('./probe.cjs');
const fs=require('node:fs');
const path=require('node:path');
async function main(){
  const records=[];
  for(const node of ['auto','cf','nya']){
    const api=`http://api.udon.dance/Api/Songs/play?${node==='auto'?'':`node=${node}&`}id=1`;
    const r=await request(api,{'User-Agent':'Mozilla/5.0'});
    const {body,...meta}=r;records.push({phase:'resolver-api',node,...meta});
    if(!r.headers?.location)continue;
    const url=new URL(r.headers.location,api).href;
    for(const [phase,headers] of [
      ['resolver-prefix',{'User-Agent':'Mozilla/5.0'}],
      ['player-open-range',{'User-Agent':'NSPlayer/12.00.26100.9457 WMFSDK/12.00.26100.9457',Range:'bytes=0-','Cache-Control':'no-cache',Pragma:'no-cache'}]
    ]){const response=await request(url,headers);const {body,...meta}=response;records.push({phase,node,...meta});}
  }
  for(const url of ['https://api.udon.dance/Api/Songs/list','https://wanna.kiva.moe/api/wannaInfo']){
    const response=await request(url,{},12*1024*1024);const {body,...meta}=response;
    const domainCounts={};
    for(const m of body.toString().matchAll(/https?:\/\/[^\s"'<>\\]+/g)){try{const h=new URL(m[0]).host;domainCounts[h]=(domainCounts[h]||0)+1;}catch{}}
    records.push({phase:'metadata',...meta,bodyURLDomainCounts:domainCounts});
  }
  fs.writeFileSync(path.join(__dirname,'game-sequence.json'),JSON.stringify({createdAt:new Date().toISOString(),note:'Resolver-style GET without Range, followed by open-ended player Range with cache-control headers from historical logs. Media transfers deliberately canceled after >=64 KiB, so capped=true is expected and not complete playback. Metadata bodies not retained.',records},null,2));
  console.log(JSON.stringify(records.map(r=>({phase:r.phase,node:r.node,url:r.url,status:r.status,bytes:r.bytes,capped:r.capped,error:r.error,domains:r.bodyURLDomainCounts})),null,2));
}
main().catch(e=>{console.error(e);process.exitCode=1;});
