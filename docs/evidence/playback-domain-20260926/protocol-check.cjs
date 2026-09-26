const {request}=require('./probe.cjs');
const fs=require('node:fs');
const path=require('node:path');
async function main(){
  const video=JSON.parse(fs.readFileSync(path.join(__dirname,'http-cf-1.json'))).hops[1].url;
  const videoPath=new URL(video).pathname+new URL(video).search;
  const examples=JSON.parse(fs.readFileSync(path.join(__dirname,'browser-observations.json'))).loadedResourceExamples;
  const targets=[['wanna.kiva.moe','/'],['api.udon.dance','/Api/Songs/play?node=cf&id=1'],['play.udon.dance',videoPath],['nya.xin.moe',videoPath],['aya.kiva.moe','/images/small1.jpg'],['static.cloudflareinsights.com',new URL(examples.find(x=>x.url.includes('static.cloudflareinsights.com')).url).pathname]];
  const records=[];
  for(const [host,p] of targets){
    const results=await Promise.all(['http','https'].map(async scheme=>{
      const r=await request(`${scheme}://${host}${p}`,{'User-Agent':'Mozilla/5.0',Range:'bytes=0-1023'},65536);
      const {body,...meta}=r;return meta;
    }));
    records.push(...results);
    console.log(JSON.stringify(results.map(r=>({url:r.url,status:r.status,bytes:r.bytes,location:r.headers?.location,tls:r.tlsAuthorized,error:r.error}))));
  }
  fs.writeFileSync(path.join(__dirname,'protocol-check.json'),JSON.stringify({createdAt:new Date().toISOString(),note:'Direct first-hop GET, no redirects followed, existing system network path; no origin substitutions; valid TLS required. Results apply to these exact sample paths.',records},null,2));
}
main().catch(e=>{console.error(e);process.exitCode=1;});
