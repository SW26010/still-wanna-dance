// Opt-in browser fixture: actual UI assets with in-memory API responses.
// No terms receipts, hosts changes, upstream traffic, media I/O, or real restart.
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { randomBytes } from 'node:crypto';
import { join } from 'node:path';
import { selectPreviewFolder } from './setup-folder-dialog.mjs';

let token = randomBytes(16).toString('hex');
const restartTimeout = process.env.SWD_UI_RESTART_TIMEOUT === '1';
const nativeFolders = process.env.SWD_UI_NATIVE_FOLDERS === '1';
const invalidTerms = process.env.SWD_UI_INVALID_TERMS === '1';
const feedbackDelay = Math.min(5000,Math.max(0,Number(process.env.SWD_UI_FEEDBACK_DELAY_MS) || 0));
const previewNotice = nativeFolders ? 'UI 预览 · 目录选择为真实 Windows 窗口，其余操作仅修改内存' : 'UI 测试夹具 · 操作仅修改内存';
let folderPending = false;
const base = new URL('../internal/console/', import.meta.url);
const portableDir = 'C:\\SWD-UI-Test', defaultStorageDir = portableDir + '\\data\\storage';
const settings = {storageDir:defaultStorageDir,maxCacheBytes:0,requestRetentionDays:30,throughputIntervalMinutes:20,queuePrefetchCount:3,downloadUpstream:'auto',upstreamMode:'direct',socks5Address:'',socks5Username:'',autoStartCDN:false,autoStartQueue:false,manualLogDir:false,logDir:'C:\\SWD-UI-Test\\logs'};
let saved = {...settings}, active = {...settings}, progress = {schemaVersion:1,step:'welcome',state:'active'};
const settingsKey = value => JSON.stringify(Object.keys(settings).sort().map(key => value[key]));
let imported = {running:false}, diagnostic = {}, checks = 0, consents = 0, application = {}, hostsReady = false, running = false, version = 1;
const receipts = new Map();
const server = createServer(async (req,res) => {
  try {
    const path = new URL(req.url,'http://localhost').pathname;
    const origin = 'http://127.0.0.1:' + server.address().port;
    if (req.headers.host !== new URL(origin).host || (req.headers.origin && req.headers.origin !== origin)) {res.writeHead(403);return res.end('fixture requires same origin');}
    res.setHeader('Cache-Control','no-store');
    res.setHeader('Content-Type','application/json; charset=utf-8');
    const json = data => res.end(JSON.stringify(data));
    if (!consents && path.startsWith('/api/') && !['/api/terms/current','/api/terms/accept','/api/terms/leave','/api/terms/return','/api/hosts/disable','/api/stop','/api/exit'].includes(path)) {
      res.writeHead(428);return json({error:'请先确认测试条款'});
    }
    if (req.method === 'POST') {
      if (req.headers['x-swd-token'] !== token) {res.writeHead(403);return json({error:'invalid fixture token'});}
      let raw = '';for await (const chunk of req) {raw += chunk;if(raw.length>8192)throw Error('body too large');}
      const input = JSON.parse(raw || '{}');
      console.log('UI_ACTION',path);
      if (feedbackDelay && ['/api/setup/storage','/api/setup/network','/api/setup/folder'].includes(path)) await new Promise(resolve=>setTimeout(resolve,feedbackDelay));
      if(path==='/api/setup/receipt') {
        if(receipts.has(input.id))return json(receipts.get(input.id));
        res.writeHead(404);return json({error:'未找到原应用回执；不会自动重新执行'});
      }
      if(path==='/api/terms/leave'||path==='/api/terms/return')return json({ok:true});
      if(path==='/api/exit')return json({ok:true});
      if(path==='/api/stop'){running=false;return json({ok:true});}
      if(path==='/api/start'){running=true;return json({ok:true});}
      if(path==='/api/setup/folder') {
        if (!nativeFolders) return json({path:input.kind==='storage'?'D:\\SWD-UI-Test\\cache':'D:\\SWD-UI-Test\\videos',canceled:false});
        if (folderPending) {res.writeHead(409);return json({error:'请先关闭已打开的目录选择窗口'});}
        folderPending = true;
        const controller = new AbortController();
        const cancel = () => controller.abort();
        res.on('close',cancel);
        try {return json(await selectPreviewFolder(input.kind,input.path,AbortSignal.any([controller.signal,AbortSignal.timeout(300000)])));}
        catch (error) {if (!res.destroyed) {res.writeHead(400);return json({error:error.message});}}
        finally {folderPending=false;res.off('close',cancel);}
        return;
      }
      if(path==='/api/setup') progress=input;
      else if(path==='/api/setup/validate') { if(input.page === 'proxy' && input.upstreamMode !== 'direct' && !input.socks5Address) {res.writeHead(400);return json({error:'请填写 SOCKS5 地址'});} }
      else if(path==='/api/setup/apply') {
        if(receipts.has(input.id))return json(receipts.get(input.id));
        if(input.baseVersion!==String(version)){res.writeHead(400);return json({error:'设置已改变，请重新进入'});}
        saved={...saved,storageDir:input.storageDir,maxCacheBytes:input.maxCacheBytes,upstreamMode:input.upstreamMode,socks5Address:input.socks5Address,socks5Username:input.socks5Username};
        if(restartTimeout) {application={id:input.id,phase:'restart',configSaved:true};receipts.set(input.id,application);return json(application);}
        active={...saved};version++;
        if(input.importSource)imported={running:true,total:10,checked:2,imported:2,phase:'测试夹具导入中',id:1};
        application={id:input.id,phase:'completed',configSaved:true,importStarted:!!input.importSource,importSource:input.importSource};receipts.set(input.id,application);progress={schemaVersion:1,step:'finish',state:'completed'};return json(application);
      }
      else if(path==='/api/hosts/enable') hostsReady=true;
      else if(path==='/api/hosts/disable') hostsReady=false;
      else if(path==='/api/settings') {version++;saved={...input,logDir:input.manualLogDir?input.logDir:settings.logDir};delete saved.customStorage;delete saved.socks5Password;}
      else if(path==='/api/restart') {active={...saved};imported={running:false};token=randomBytes(16).toString('hex');}
      else if(path==='/api/import/start') imported={running:true,total:10,processed:2,imported:2,phase:'测试夹具导入中',id:1};
      else if(path==='/api/setup/storage') return json({path:input.path,writable:{state:'ok',message:'测试目录可写'},library:{state:'ok',message:'测试新库；未实际创建'},space:{state:'ok',message:'测试分区容量'},freeBytes:120*2**30,totalBytes:512*2**30});
      else if(path==='/api/setup/logs') return json({state:'ok',message:'已检测到可读取的 VRC 日志。',directory:active.logDir,path:active.logDir+'\\output_log_2026-10-12_12-00-00.txt'});
      else if(['/api/setup/network','/api/setup/network/catalog','/api/setup/network/playback','/api/setup/network/throughput','/api/terms/accept'].includes(path)) {
        if(path==='/api/terms/accept') {
          if(!input.agree || !input.contentRights || !input.version || !input.sha256) {res.writeHead(400);return res.end('请主动确认测试条款');}
          consents++;
        }
        checks++;
        const now = new Date().toISOString();
        const operations = path.endsWith('/catalog') ? ['catalog'] : path.endsWith('/playback') ? ['playback_url'] : path.endsWith('/throughput') ? ['resource'] : ['catalog','playback_url','resource'];
        const results = (diagnostic.monitor?.results || []).filter(r=>!operations.includes(r.operation));
        for (const operation of operations) results.push({operation,route:'fixture.invalid',entry:'https://fixture.invalid',mode:'direct',ip:'192.0.2.1',channelID:'fixture-direct',state:'available',observedAt:now,validUntil:new Date(Date.now()+360000).toISOString(),estimatedLatencyMS:22,...(operation==='resource'?{estimatedSpeedBPS:4*2**20,transferredBytes:4*2**20,transferDurationMS:1000,throughputObservedAt:now,throughputSongID:42}:{})});
        diagnostic={running:false,monitor:{finished:now,results}};
      } else {res.writeHead(409);return json({error:'此操作未由 UI 夹具实现，不执行真实动作'});}
      return json({ok:true});
    }
    if(path==='/api/status') return json({running,settings:saved,activeSettings:active,restartRequired:settingsKey(saved)!==settingsKey(active),hosts:{ready:hostsReady},setupSettingsVersion:String(version),setupApplication:application,portOK:true,httpsPortOK:true,queue:{},batch:{},traffic:{},import:imported,setupNetwork:diagnostic,upstreamMonitor:{results:[]},defaultLogDir:settings.logDir,defaultStorageDir,portableDir});
    if(path==='/api/terms/current') return json({text:await readFile(new URL('../legal/TERMS.txt',base),'utf8'),version:'fixture-only',sha256:'fixture-only',accepted:consents>0,needsConfirmation:invalidTerms && !consents});
    if(path==='/terms.txt') {res.setHeader('Content-Type','text/plain; charset=utf-8');return res.end(await readFile(new URL('../legal/TERMS.txt',base)));}
    if(path==='/api/setup') return json(progress);
    if(path==='/api/inventory') return json({});
    if(path==='/api/downloads') return json({running:false,tasks:[],bytesPerSecond:0});
    if(path.startsWith('/api/requests')) return json({requests:[],hasMore:false});
    if(path==='/__fixture/state') return json({progress,saved,active,checks,consents,imported,hostsReady,running,application});
    if(!consents && ['/__fixture/welcome','/__fixture/terms'].includes(path) && process.env.SWD_UI_TERMS_EXPORT_DIR) {
      const html=await readFile(join(process.env.SWD_UI_TERMS_EXPORT_DIR,path.endsWith('/welcome')?'welcome.html':'terms.html'),'utf8');
      res.setHeader('Content-Type','text/html; charset=utf-8');
      return res.end(html.replaceAll('__TOKEN__',token).replace('<div class="setup-heading">','<div class="setup-heading"><p class="setup-note" role="note">'+previewNotice+'</p>'));
    }
    if(['/','/__fixture/welcome','/__fixture/terms'].includes(path)) {
      const [index,setup]=await Promise.all([readFile(new URL('index.html',base),'utf8'),readFile(new URL('setup.html',base),'utf8')]);
      res.setHeader('Content-Type','text/html; charset=utf-8');
      return res.end(index.replace('__SETUP__',setup.replace('<div class="setup-heading">','<div class="setup-heading"><p class="setup-note" role="note">'+previewNotice+'</p>')).replaceAll('__TOKEN__',token).replace('__TERMS_GATE__',JSON.stringify(consents?null:{step:'welcome',pageID:'fixture-page'})));
    }
    if(path==='/help') {
      let html=await readFile(new URL('help.html',base),'utf8');
      for(const [key,value] of Object.entries({Config:'C:\\SWD-UI-Test\\data\\config.json',Root:'C:\\SWD-UI-Test\\data',Storage:active.storageDir,Log:'C:\\SWD-UI-Test\\data\\logs\\app.log'}))html=html.replaceAll('{{.'+key+'}}',value);
      res.setHeader('Content-Type','text/html; charset=utf-8');return res.end(html);
    }
    const assets=new Set(['/assets/console.js','/assets/navigation.js','/assets/cache.js','/assets/setup.js','/assets/console.css','/assets/storage-benefit.svg']);
    if(assets.has(path)) {res.setHeader('Content-Type',path.endsWith('.js')?'text/javascript; charset=utf-8':path.endsWith('.svg')?'image/svg+xml':'text/css; charset=utf-8');return res.end(await readFile(new URL(path.slice(1),base)));}
    res.writeHead(404);res.end('fixture route not found');
  } catch(error) {if (!res.headersSent) res.writeHead(500);if (!res.writableEnded) res.end(String(error));}
});
server.listen(Number(process.env.SWD_UI_PORT || 0),'127.0.0.1',()=>console.log('FIXTURE_URL=http://127.0.0.1:'+server.address().port));
