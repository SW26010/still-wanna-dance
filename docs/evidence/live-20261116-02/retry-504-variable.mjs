// THROWAWAY: one controlled media/client only. Not a product retry policy.
import http from 'node:http';
import assert from 'node:assert/strict';
import {pathToFileURL} from 'node:url';
import fs from 'node:fs';

export class RetryGate {
  constructor({enabled=false,now=()=>performance.now(),hole=2*1048576,releaseMs=66000,minStallMs=25000,retryMs=2000}={}) {
    Object.assign(this,{enabled,now,hole,releaseMs,minStallMs,retryMs});
    this.zero=null;this.previous=null;this.used=false;
  }
  start(){this.zero??=this.now();}
  observe({start,bytes,lastWrite,ended,complete}) {
    if(!complete&&bytes>0&&start+bytes===this.hole&&ended-lastWrite>=this.minStallMs)
      this.previous={ended,lastWrite};
  }
  reject({method,range}) {
    const m=/^bytes=(\d+)-(\d*)$/.exec(range??'');
    if(!this.enabled||this.used||!this.previous||!m||method!=='GET'||this.zero===null)return false;
    const start=Number(m[1]),end=m[2]===''?Infinity:Number(m[2]),age=this.now()-this.previous.ended;
    if(start<this.hole-8192||start>this.hole||end<this.hole||age<0||age>this.retryMs||this.now()-this.zero>=this.releaseMs)return false;
    this.used=true;return true;
  }
}

export function createProxy({upstream,gate,song="4598",log=()=>{}}) {
  const origin=new URL(upstream);
  if(origin.protocol!=='http:'||origin.hostname!=='127.0.0.1')throw Error('isolated loopback upstream required');
  let sequence=0;
  return http.createServer((req,res)=>{
    const u=new URL(req.url,'http://127.0.0.1');
    if(u.pathname!=='/Api/Songs/play'||u.searchParams.get('id')!==song) {res.writeHead(404);res.end();return;}
    const id=++sequence,player=req.method==='GET'&&String(req.headers['user-agent']??'').startsWith('NSPlayer/');
    const relative=()=>gate.zero===null?null:Math.round(gate.now()-gate.zero)/1000;
    if(player)gate.start();
    log({event:'request',id,t:relative(),player,range:req.headers.range??''});
    if(player&&gate.reject({method:req.method,range:req.headers.range})) {
      log({event:'injected-504',id,t:relative(),onceOnly:true,SWDDemandCreated:false});
      res.writeHead(504,{'content-type':'text/plain; charset=utf-8','content-length':16,'cache-control':'no-store'});res.end('Gateway Timeout\n');return;
    }
    let bytes=0,lastWrite=null,finished=false,observed=false,source;
    const start=Number(/^bytes=(\d+)-/.exec(req.headers.range??'')?.[1]??0);
    const observe=()=>{
      if(observed)return;observed=true;
      if(player&&[200,206].includes(res.statusCode))gate.observe({start,bytes,lastWrite,ended:gate.now(),complete:finished});
      log({event:'response-ended',id,t:relative(),bytes,complete:finished,armed:!!gate.previous});
    };
    const outgoing=http.request({hostname:origin.hostname,port:origin.port,path:req.url,method:req.method,headers:req.headers},incoming=>{
      source=incoming;res.writeHead(incoming.statusCode,incoming.headers);
      incoming.on('data',chunk=>{
        bytes+=chunk.length;lastWrite=gate.now();
        if(!res.write(chunk))incoming.pause();
      });
      res.on('drain',()=>incoming.resume());
      incoming.on('end',()=>{finished=true;res.end();});
      incoming.on('aborted',()=>res.destroy());
      incoming.on('error',()=>res.destroy());
    });
    outgoing.on('error',()=>{if(!res.headersSent){res.writeHead(502);res.end();}else res.destroy();});
    res.on('close',()=>{observe();source?.destroy();outgoing.destroy();});
    req.on('aborted',()=>outgoing.destroy());req.pipe(outgoing);
  });
}

async function selfCheck(){
  let now=0;
  const make=(enabled=true)=>{const g=new RetryGate({enabled,now:()=>now});now=0;g.start();now=31000;g.observe({start:0,bytes:2097152,lastWrite:0,ended:now,complete:false});return g;};
  const exact={method:'GET',range:'bytes=2097152-4194303'};
  let g=make();assert(g.reject(exact));assert(!g.reject(exact));
  g=make();assert(g.reject({...exact,range:'bytes=2088960-4194303'}));
  for(const r of [{...exact,range:'bytes=3145728-'},{...exact,range:'bytes=0-'},{...exact,range:'bytes=2088960-2097151'},{...exact,method:'HEAD'}]){g=make();assert(!g.reject(r));}
  g=make(false);assert(!g.reject(exact));g=make();now=66000;assert(!g.reject(exact));
  g=make();now+=2001;assert(!g.reject(exact));
  g=new RetryGate({enabled:true,now:()=>0});g.start();g.observe({start:0,bytes:2097152,lastWrite:0,ended:1000,complete:false});assert(!g.reject(exact));
  g=new RetryGate({enabled:true,now:()=>31000});g.start();g.observe({start:0,bytes:2097152,lastWrite:0,ended:31000,complete:true});assert(!g.reject(exact));
  const upstream=http.createServer((req,res)=>{
    if(req.headers.range==='bytes=0-'){res.writeHead(206,{'content-length':4194304,'content-range':'bytes 0-4194303/4194304'});res.write(Buffer.alloc(2097152));}
    else {res.writeHead(503);res.end('origin reached');}
  });
  await new Promise(r=>upstream.listen(0,'127.0.0.1',r));
  now=0;g=new RetryGate({enabled:true,now:()=>now});let ended;const endObserved=new Promise(r=>ended=r);
  const proxy=createProxy({upstream:`http://127.0.0.1:${upstream.address().port}`,gate:g,log:e=>{if(e.event==='response-ended')ended();}});
  await new Promise(r=>proxy.listen(0,'127.0.0.1',r));
  const url=`http://127.0.0.1:${proxy.address().port}/Api/Songs/play?id=4598`;
  try {
    await new Promise((resolve,reject)=>{
      const request=http.get(url,{headers:{'user-agent':'NSPlayer/lab','range':'bytes=0-'}},response=>{
        let count=0;response.on('data',b=>{count+=b.length;if(count===2097152){now=31000;response.destroy();resolve();}});response.on('error',()=>{});
      });request.on('error',reject);
    });
    await endObserved;
    const get=()=>new Promise((resolve,reject)=>http.get(url,{headers:{'user-agent':'NSPlayer/lab',range:exact.range}},r=>{const status=r.statusCode;r.resume();r.on('end',()=>resolve(status));}).on('error',reject));
    assert.equal(await get(),504);assert.equal(await get(),503);
  }finally{proxy.closeAllConnections();upstream.closeAllConnections();await Promise.all([new Promise(r=>proxy.close(r)),new Promise(r=>upstream.close(r))]);}
  console.log(JSON.stringify({checks:'exact range, 8KiB backtrack, unrelated ranges, HEAD, disabled, restored, stale, short stall, completed response, real HTTP one-shot 504 and subsequent pass-through',passed:true,gameBehaviorVerified:false}));
}

if(process.argv[1]&&import.meta.url===pathToFileURL(process.argv[1]).href){
  if(process.argv.includes('--self-check'))await selfCheck();
  else {
    const upstream=process.env.LAB51_PROXY_UPSTREAM;
    if(!upstream)throw Error('LAB51_PROXY_UPSTREAM is required');
    const song=process.env.LAB51_SONG??'4598';
    const hole=Number(process.env.LAB51_WARM_BLOCKS??2)*1048576;
    const gate=new RetryGate({enabled:process.env.LAB51_EARLY_504==='1',hole});
    const proxy=createProxy({upstream,gate,song,log:e=>console.log(JSON.stringify({at:new Date().toISOString(),...e}))});
    proxy.listen(Number(process.env.LAB51_PROXY_PORT??19083),'127.0.0.1',()=>console.log(JSON.stringify({event:'ready',early504:gate.enabled,listen:proxy.address(),song,holeBytes:hole,scope:'single controlled player; release at66s; no production handoff for injected504'})));
    for(const signal of ['SIGINT','SIGTERM'])process.on(signal,()=>{proxy.closeAllConnections();proxy.close();});
    const stopFile=process.env.LAB51_PROXY_STOP_FILE;
    if(stopFile){const watch=setInterval(()=>{if(fs.existsSync(stopFile)){clearInterval(watch);proxy.closeAllConnections();proxy.close();}},200);watch.unref();}
    const watchdog=setTimeout(()=>{proxy.closeAllConnections();proxy.close();},45*60*1000);watchdog.unref();
  }
}
