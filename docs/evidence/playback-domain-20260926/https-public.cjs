const {request}=require('./probe.cjs');
const fs=require('node:fs');
const path=require('node:path');
const out=__dirname;
async function main(){
  const records=[];
  for(const node of ['cf','nya']) {
    const previous=JSON.parse(fs.readFileSync(path.join(out,`https-${node}-1.json`)));
    let url=previous.hops[1].url.replace(/^http:/,'https:');
    const hops=[];
    for(let i=0;i<8;i++) {
      const host=new URL(url).hostname;
      const dns=await request('https://dns.google/resolve?name='+host+'&type=A');
      const answer=JSON.parse(dns.body.toString());
      const ip=answer.Answer?.find(x=>x.type===1)?.data;
      if(!ip) throw new Error('No A answer for '+host);
      const r=await request(url,{'User-Agent':'Mozilla/5.0',Range:'bytes=0-1023','Accept-Encoding':'identity'},65536,{lookup:(_h,opts,cb)=>opts.all?cb(null,[{address:ip,family:4}]):cb(null,ip,4)});
      const {body,...meta}=r;hops.push({...meta,chosenPublicIP:ip,dnsAnswer:answer});
      if(r.status>=300&&r.status<400&&r.headers.location){url=new URL(r.headers.location,url).href;continue;}
      break;
    }
    records.push({label:'public-doh-https-'+node,hops});
    console.log(JSON.stringify(records.at(-1)));
  }
  fs.writeFileSync(path.join(out,'public-doh-https.json'),JSON.stringify({createdAt:new Date().toISOString(),note:'Explicit HTTPS counterpart of observed API video URL, connecting to Google DoH A answer; original Host/SNI and TLS validation retained. Does not prove absence of transparent network interception.',records},null,2));
  const sha=await request('https://139.196.46.195:51886/Api/Songs/play?id=1',{'User-Agent':'NSPlayer/12.00.26100.9457 WMFSDK/12.00.26100.9457',Range:'bytes=0-1023'});
  const {body,...meta}=sha;
  fs.writeFileSync(path.join(out,'sha-entry.json'),JSON.stringify({...meta,note:'Entry copied from previously observed game logs; not a URL discovered in this browser session.'},null,2));
  console.log('SHA',meta.status,meta.error);
}
main().catch(e=>{console.error(e);process.exitCode=1;});
