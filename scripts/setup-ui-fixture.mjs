// Opt-in browser fixture: actual UI assets with in-memory API responses.
// No terms receipts, hosts changes, upstream traffic, media I/O, or real restart.
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { randomBytes } from 'node:crypto';

let token = randomBytes(16).toString('hex');
const restartTimeout = process.env.SWD_UI_RESTART_TIMEOUT === '1';
const base = new URL('../internal/console/', import.meta.url);
const portableDir = 'C:\\SWD-UI-Test', defaultStorageDir = portableDir + '\\data\\storage';
const settings = {storageDir:defaultStorageDir,maxCacheBytes:0,requestRetentionDays:30,throughputIntervalMinutes:20,queuePrefetchCount:3,downloadUpstream:'auto',upstreamMode:'direct',socks5Address:'',socks5Username:'',autoStartCDN:false,autoStartQueue:false,manualLogDir:false,logDir:'C:\\SWD-UI-Test\\logs'};
let saved = {...settings}, active = {...settings}, progress = {schemaVersion:1,step:'welcome',state:'active'};
const settingsKey = value => JSON.stringify(Object.keys(settings).sort().map(key => value[key]));
let imported = {running:false}, diagnostic = {}, checks = 0, consents = 0, application = {}, hostsReady = false, version = 1;
const server = createServer(async (req,res) => {
  try {
    const path = new URL(req.url,'http://localhost').pathname;
    res.setHeader('Cache-Control','no-store');
    const json = data => {res.setHeader('Content-Type','application/json');res.end(JSON.stringify(data));};
    if (req.method === 'POST') {
      if (req.headers['x-swd-token'] !== token) {res.writeHead(403);return json({error:'invalid fixture token'});}
      let raw = '';for await (const chunk of req) {raw += chunk;if(raw.length>8192)throw Error('body too large');}
      const input = JSON.parse(raw || '{}');
      console.log('UI_ACTION',path);
      if(path==='/api/setup') progress=input;
      else if(path==='/api/setup/validate') { if(input.page === 'proxy' && input.upstreamMode !== 'direct' && !input.socks5Address) {res.writeHead(400);return json({error:'请填写 SOCKS5 地址'});} }
      else if(path==='/api/setup/apply') {
        if(application.id===input.id)return json(application);
        if(input.baseVersion!==String(version)){res.writeHead(400);return json({error:'设置已改变，请重新进入'});}
        saved={...saved,storageDir:input.storageDir,maxCacheBytes:input.maxCacheBytes,upstreamMode:input.upstreamMode,socks5Address:input.socks5Address,socks5Username:input.socks5Username};
        if(restartTimeout) {application={id:input.id,phase:'restart',configSaved:true};return json(application);}
        active={...saved};version++;
        if(input.importSource)imported={running:true,total:10,checked:2,imported:2,phase:'测试夹具导入中',id:1};
        application={id:input.id,phase:'completed',configSaved:true,importStarted:!!input.importSource,importSource:input.importSource};progress={schemaVersion:1,step:'finish',state:'completed'};return json(application);
      }
      else if(path==='/api/hosts/enable') hostsReady=true;
      else if(path==='/api/hosts/disable') hostsReady=false;
      else if(path==='/api/settings') {version++;saved={...input,logDir:input.manualLogDir?input.logDir:settings.logDir};delete saved.customStorage;delete saved.socks5Password;}
      else if(path==='/api/restart') {active={...saved};imported={running:false};token=randomBytes(16).toString('hex');}
      else if(path==='/api/import/start') imported={running:true,total:10,processed:2,imported:2,phase:'测试夹具导入中',id:1};
      else if(path==='/api/setup/storage') return json({path:input.path,writable:{state:'ok',message:'测试目录可写'},library:{state:'ok',message:'测试新库；未实际创建'},space:{state:'ok',message:'测试分区容量'},freeBytes:120*2**30,totalBytes:512*2**30});
      else if(path==='/api/setup/logs') return json({state:'ok',message:'已检测到可读取的 VRC 日志。',directory:active.logDir,path:active.logDir+'\\output_log_2026-10-12_12-00-00.txt'});
      else if(path==='/api/setup/network' || path==='/api/terms/accept') {
        if(path==='/api/terms/accept') {
          if(!input.agree || !input.contentRights || input.version!=='fixture-only' || input.sha256!=='fixture-only') {res.writeHead(400);return res.end('请主动确认测试条款');}
          consents++;
        }
        checks++;
        const now = new Date().toISOString();
        diagnostic={running:false,monitor:{finished:now,results:[{operation:'resource',route:'fixture.invalid',entry:'https://fixture.invalid',mode:'direct',ip:'192.0.2.1',channelID:'fixture-direct',state:'available',observedAt:now,validUntil:new Date(Date.now()+360000).toISOString(),estimatedLatencyMS:22,estimatedSpeedBPS:4*2**20,transferredBytes:4*2**20,transferDurationMS:1000,throughputObservedAt:now,throughputSongID:42}]}};
      } else {res.writeHead(409);return json({error:'此操作未由 UI 夹具实现，不执行真实动作'});}
      return json({ok:true});
    }
    if(path==='/api/status') return json({running:false,settings:saved,activeSettings:active,restartRequired:settingsKey(saved)!==settingsKey(active),hosts:{ready:hostsReady},setupSettingsVersion:String(version),setupApplication:application,portOK:true,httpsPortOK:true,queue:{},batch:{},traffic:{},import:imported,setupNetwork:diagnostic,upstreamMonitor:{results:[]},defaultLogDir:settings.logDir,defaultStorageDir,portableDir});
    if(path==='/api/terms/current') return json({text:await readFile(new URL('../legal/TERMS.txt',base),'utf8'),version:'fixture-only',sha256:'fixture-only',accepted:consents>0});
    if(path==='/terms.txt') {res.setHeader('Content-Type','text/plain; charset=utf-8');return res.end(await readFile(new URL('../legal/TERMS.txt',base)));}
    if(path==='/api/setup') return json(progress);
    if(path==='/api/inventory') return json({});
    if(path==='/api/downloads') return json({running:false,tasks:[],bytesPerSecond:0});
    if(path.startsWith('/api/requests')) return json({requests:[],hasMore:false});
    if(path==='/__fixture/state') return json({progress,saved,active,checks,consents,imported,hostsReady,application});
    if(path==='/') {
      const [index,setup]=await Promise.all([readFile(new URL('index.html',base),'utf8'),readFile(new URL('setup.html',base),'utf8')]);
      res.setHeader('Content-Type','text/html; charset=utf-8');
      return res.end(index.replace('__SETUP__',setup).replaceAll('__TOKEN__',token).replace('<body>','<body><p role="note">UI 测试夹具：所有操作仅修改内存，不代表真实网络或游戏验收。</p>'));
    }
    if(path==='/help') {
      let html=await readFile(new URL('help.html',base),'utf8');
      for(const [key,value] of Object.entries({Config:'C:\\SWD-UI-Test\\data\\config.json',Root:'C:\\SWD-UI-Test\\data',Storage:active.storageDir,Log:'C:\\SWD-UI-Test\\data\\logs\\app.log'}))html=html.replaceAll('{{.'+key+'}}',value);
      res.setHeader('Content-Type','text/html; charset=utf-8');return res.end(html);
    }
    const assets=new Set(['/assets/console.js','/assets/navigation.js','/assets/cache.js','/assets/setup.js','/assets/console.css','/assets/storage-benefit.svg']);
    if(assets.has(path)) {res.setHeader('Content-Type',path.endsWith('.js')?'text/javascript; charset=utf-8':path.endsWith('.svg')?'image/svg+xml':'text/css; charset=utf-8');return res.end(await readFile(new URL(path.slice(1),base)));}
    res.writeHead(404);res.end('fixture route not found');
  } catch(error) {res.writeHead(500);res.end(String(error));}
});
server.listen(Number(process.env.SWD_UI_PORT || 0),'127.0.0.1',()=>console.log('FIXTURE_URL=http://127.0.0.1:'+server.address().port));
