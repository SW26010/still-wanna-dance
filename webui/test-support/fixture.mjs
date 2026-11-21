// Memory-only browser test server. Never starts the app, touches hosts or media.
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
// Reuse the ws implementation shipped with our pinned Playwright test runtime.
const requirePlaywright = createRequire(createRequire(import.meta.resolve('@playwright/test')).resolve('playwright'));
const { wsServer: WebSocketServer } = requirePlaywright('playwright-core/lib/utilsBundle');
const root = new URL('../../internal/console/', import.meta.url);
const html = await readFile(new URL('webui.html', root), 'utf8');
const token = 'browser-fixture-token';
const streams = new Map(), states = new Map();
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1:18473');
  const p = url.pathname;
  res.setHeader('Cache-Control', 'no-store');
  res.setHeader('Content-Security-Policy', "default-src 'self'; img-src 'self' blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'");
  try {
    if(p==='/__fixture/events'&&req.method==='POST'){
      let raw='';for await(const chunk of req)raw+=chunk;
      const {id,kind,state}=JSON.parse(raw), key=id+':'+kind;
      if(kind==='blocks'){
        const blocks=new Map((state.reset?[]:states.get(key)?.blocks||[]).map(b=>[b.id,b]));
        for(const block of state.blocks)blocks.set(block.id,block);
        for(const removed of state.removed)blocks.delete(removed);
        const demands=new Map((state.reset?[]:states.get(key)?.demands||[]).map(d=>[d.id,d]));
        for(const demand of state.demands||[])demands.set(demand.id,demand);
        for(const removed of state.removedDemands||[])demands.delete(removed);
        const availability={...(state.reset?{}:states.get(key)?.availability),...state.availability};
        for(const resource of state.removedAvailability||[])delete availability[resource];
        states.set(key,{...state,reset:true,blocks:[...blocks.values()],removed:[],demands:[...demands.values()],removedDemands:[],availability,removedAvailability:[]});
      }else states.set(key,state);
      const eventName=kind.startsWith('block-detail:')?'block-detail':kind;
      for(const stream of streams.get(['download-speed','block-cache','closed'].includes(kind)?id+':blocks':key)||[])stream.send(JSON.stringify({event:eventName,data:state}));
      res.setHeader('Content-Type','application/json');return res.end('{"ok":true}');
    }
    if(p==='/__fixture/streams'){
      res.setHeader('Content-Type','application/json');
      return res.end(JSON.stringify(Object.fromEntries([...streams].filter(([key])=>key.startsWith(url.searchParams.get('id')+':')).map(([key,value])=>[key.slice(key.indexOf(':')+1),value.size]))));
    }
    if(p==='/__fixture/disconnect'&&req.method==='POST'){
      let raw='';for await(const chunk of req)raw+=chunk;
      const {id,kind}=JSON.parse(raw);for(const stream of streams.get(id+':'+kind)||[])stream.close();
      return res.end('{}');
    }
    if (p.startsWith('/assets/')) {
      const file = p === '/assets/logo.svg' ? new URL('../branding/logo.svg', root) : new URL(p.slice(1), root);
      if (!p.match(/^\/assets\/(webui\/(app\.(js|css)|category-covers\/[a-z0-9-]+\.webp)|logo\.svg|storage-benefit\.svg)$/)) { res.writeHead(404); return res.end(); }
      res.setHeader('Content-Type', p.endsWith('.js') ? 'text/javascript' : p.endsWith('.css') ? 'text/css' : p.endsWith('.webp') ? 'image/webp' : 'image/svg+xml');
      return res.end(await readFile(file));
    }
    if (p === '/favicon.ico' || p === '/favicon-light.ico') {
      res.setHeader('Content-Type', 'image/x-icon');
      return res.end(await readFile(new URL(p === '/favicon-light.ico' ? '../branding/logo-light.ico' : '../branding/logo.ico', root)));
    }
    if (p.startsWith('/api/')) {
      if (req.headers['x-swd-token'] !== token) { res.writeHead(403); return res.end('invalid token'); }
      res.writeHead(404); return res.end('not found');
    }
    // Consent follows the browser session, including the wizard's help popup.
    const gate=url.searchParams.has('gate')||/(?:^|;\s*)fixture-gate=1(?:;|$)/.test(req.headers.cookie||'');
    if(gate)res.setHeader('Set-Cookie','fixture-gate=1; Path=/');
    const publicDocument=gate&&['/help','/about'].includes(p);
    const data = {settingsDefaults:{prepareAheadSeconds:500,downloadConcurrencySoftLimit:16,downloadGrowIntervalMS:250,localProtectionGraceSeconds:300,logBudgetBytes:20*1048576},build: {kind:'development', version:'dev-browser', revision:'a'.repeat(40), dirty:true, builtAtUtc:'2026-10-19T00:00:00Z', toolchain:'go1.25.0', target:'windows/amd64'}, page: publicDocument?p.slice(1):'console', token, termsGate:gate&&!publicDocument?{pageID:'fixture-page'}:null, paths: {Config: 'C:\\UI-Test\\config.json', Root: 'C:\\UI-Test', State: 'C:\\UI-Test\\data\\state', Work: 'C:\\UI-Test\\work\\console-test', Database: '<script>alert("path")</script>', Videos: 'D:\\Videos', Log: 'C:\\UI-Test\\app.log'}, terms: {text: '浏览器测试条款\n软件许可不等于内容授权。',version: 'fixture'}};
    if(url.searchParams.has('recovery'))data.recovery={reason:'测试启动失败'};
    const json = JSON.stringify(data).replaceAll('<', '\\u003c').replaceAll('>', '\\u003e').replaceAll('&', '\\u0026');
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(html.replaceAll('{{.Title}}', 'UI 测试').replaceAll('{{.Token}}', token).replace('{{.Data}}', json));
  } catch { res.writeHead(500); res.end('fixture error'); }
});
const sockets = new WebSocketServer({noServer:true});
server.on('upgrade',(req,socket,head)=>{
  const url=new URL(req.url,'http://127.0.0.1:18473');
  if(!['/api/history/events','/api/block-demands/events'].includes(url.pathname)){socket.destroy();return;}
  sockets.handleUpgrade(req,socket,head,stream=>{
    const id=req.headers.cookie?.match(/(?:^|;\s*)fixture-id=([^;]+)/)?.[1]||'default';
    const block=url.searchParams.get('block'),history=url.pathname==='/api/history/events';
    const kind=history?'history':block?'block-detail:'+block:'blocks',key=id+':'+kind;
    const fallback=history?{closed:false}:block?null:{at:Date.now(),running:false,reset:true,capacity:4096,used:0,active:0,open:0,prepareAheadMS:500000,downloadGraceMS:30000,blocks:[],removed:[],demands:[],removedDemands:[]};
    const send=(event,data)=>stream.send(JSON.stringify({event,data}));
    send(block?'block-detail':kind,states.has(key)?states.get(key):fallback);
    if(!history&&!block){
      if(states.get(id+':download-speed')?.bytesPerSecond)send('download-speed',states.get(id+':download-speed'));
      if(states.has(id+':block-cache'))send('block-cache',states.get(id+':block-cache'));
    }
    if(!streams.has(key))streams.set(key,new Set());streams.get(key).add(stream);
    stream.on('close',()=>streams.get(key)?.delete(stream));
  });
});
server.listen(18473,'127.0.0.1');
