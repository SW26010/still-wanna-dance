// Read-only playback probes. No StepStash transport or origin mappings used.
const http = require('node:http');
const https = require('node:https');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const out = __dirname;
const ua = 'NSPlayer/12.00.26100.9457 WMFSDK/12.00.26100.9457';
function request(url, headers = {}, cap = 65536, options = {}) {
  return new Promise(resolve => {
    const started = Date.now();
    const result = {url, startedAt: new Date(started).toISOString(), requestHeaders: headers};
    let done = false, chunks = [], bytes = 0;
    const finish = extra => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      const body = Buffer.concat(chunks);
      resolve({...result, elapsedMS: Date.now() - started, bytes, sha256: crypto.createHash('sha256').update(body).digest('hex'), ...extra, body});
    };
    const req = (url.startsWith('https:') ? https : http).get(url, {headers,...options}, res => {
      result.status = res.statusCode;
      result.headers = res.headers;
      result.remoteAddress = res.socket.remoteAddress;
      result.tlsAuthorized = res.socket.encrypted ? res.socket.authorized : null;
      if(res.socket.encrypted) { const cert=res.socket.getPeerCertificate(); result.certificate={subject:cert.subject,issuer:cert.issuer,subjectaltname:cert.subjectaltname,valid_to:cert.valid_to}; }
      res.on('data', chunk => { chunks.push(chunk); bytes += chunk.length; if (bytes >= cap) { finish({capped:true}); res.destroy(); } });
      res.on('end', () => finish({complete:true}));
      res.on('error', error => finish({error:error.message}));
    });
    const timer = setTimeout(() => { finish({error:'20 second deadline'}); req.destroy(); }, 20000);
    req.on('error', error => finish({error:error.message, code:error.code}));
  });
}
async function chain(label, url, range='bytes=0-1023', userAgent=ua) {
  const hops = [];
  for(let i=0;i<8;i++) {
    const r = await request(url, {'User-Agent':userAgent,Range:range,'Accept-Encoding':'identity'});
    const {body,...evidence} = r;
    hops.push(evidence);
    if(r.status >= 300 && r.status < 400 && r.headers.location) {url = new URL(r.headers.location,url).href;continue;}
    break;
  }
  const record = {label,hops};
  fs.writeFileSync(path.join(out,label+'.json'),JSON.stringify(record,null,2));
  console.log(JSON.stringify({label,chain:hops.map(h=>({url:h.url,status:h.status,bytes:h.bytes,error:h.error,location:h.headers?.location}))}));
  return record;
}
async function main() {
  const jobs = [];
  for(const id of [1,1343,6927]) for(const scheme of ['http','https']) for(const node of ['auto','cf','nya']) {
    jobs.push(()=>chain(`${scheme}-${node}-${id}`,`${scheme}://api.udon.dance/Api/Songs/play?${node==='auto'?'':`node=${node}&`}id=${id}`));
  }
  const records=[];
  async function worker(){while(jobs.length) records.push(await jobs.shift()());}
  await Promise.all([worker(),worker(),worker()]);
  records.push(await chain('http-cf-1-middle','http://api.udon.dance/Api/Songs/play?node=cf&id=1','bytes=20000000-20001023'));
  records.push(await chain('https-web-auto-1','https://api.udon.dance/Api/Songs/play?id=1','bytes=0-1023','Mozilla/5.0'));
  fs.writeFileSync(path.join(out,'summary.json'),JSON.stringify({createdAt:new Date().toISOString(),notes:['Native Node HTTP/HTTPS, default certificate verification, OS resolver, no HTTP proxy environment support, transparent proxy/TUN may apply.','Range GET simulation; not actual VRChat execution; no hardcoded origin replacement.'],records},null,2));
  for(const host of ['wanna.kiva.moe','api.udon.dance','play.udon.dance','nya.xin.moe','aya.kiva.moe']) {
    const r=await request('https://dns.google/resolve?name='+host+'&type=A');
    const {body,...meta}=r;
    fs.writeFileSync(path.join(out,'dns-'+host+'.json'),JSON.stringify({...meta,answer:body.toString()},null,2));
    console.log('DNS',host,body.toString().slice(0,180),r.error||'');
  }
}
module.exports={request,chain};
if(require.main===module) main().catch(e=>{console.error(e);process.exitCode=1;});
