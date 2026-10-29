import type { Page } from '@playwright/test';
import type { CacheEntry, Settings, Status } from '../src/types';
export async function choosePath(page: Page, label: string, path: string) {
  await page.route('**/api/path/select', route => route.fulfill({json:{path,canceled:false}}), {times:1});
  const response = page.waitForResponse('**/api/path/select');
  await page.getByRole('textbox',{name:label,exact:true}).click();
  await response;
}
export function statusFixture(): Status {
  const settings: Settings = {theme:'system',logLevel:'info',retainFavorites:true,databasePath:'C:\\UI-Test\\storage\\storage.sqlite',videoDir:'D:\\Videos',videoBudgetBytes:40*1073741824,videoStorageMode:'unlimited',aggressiveBlockReclamation:'auto',requestRetentionMode:'unlimited',requestRetentionDays:0,throughputIntervalMinutes:20,prepareAheadSeconds:500,downloadConcurrencySoftLimit:16,downloadGrowIntervalMS:250,localProtectionGraceSeconds:300,upstreamMode:'direct',proxyBackend:'external-socks5',socks5Address:'',socks5Username:'',autoAccelerate:false,autoOpenWebUI:true,queueWithGateway:true,restoreHostsOnStopOrExit:true,vrchatLogDir:''};
  return {vrcxImportAvailable:true,videoImportSuggested:false,running:false,settings:{...settings},activeSettings:{...settings},restartRequired:false,hosts:{ready:false,message:'尚未接入'},portOK:true,httpsPortOK:true,batch:{},lastBatch:{},import:{},queue:{},defaultLogDir:'C:\\UI-Test\\logs',defaultDatabasePath:settings.databasePath,defaultVideoDir:settings.videoDir,portableDir:'C:\\UI-Test',activation:{},upstreamMonitor:{results:[]}};
}
export async function mockAPI(page: Page) {
  await page.routeWebSocket('**/api/lifecycle',socket=>socket.send(JSON.stringify({action:'running'})));
  const fixtureID=crypto.randomUUID();
  await page.context().addCookies([{name:'fixture-id',value:fixtureID,url:'http://127.0.0.1:18473'}]);
  const state = statusFixture(), calls: {path:string;body:Record<string,unknown>;token?:string}[] = [];
  const terms={text:'测试完整条款。软件许可不等于内容授权。',version:'fixture',sha256:'fixture-hash',accepted:true,needsConfirmation:false};
  const diagnostics={connectivity:{revision:1,run:0,upstream:'direct',phase:'idle',running:false,results:[],lastValid:{}},calibration:{revision:1,run:0,upstream:'direct',phase:'idle',running:false,lastValid:{},referenceBitrateBPS:2000000}};
  const emit=async(kind:keyof typeof diagnostics)=>{await page.request.post('/__fixture/events',{data:{id:fixtureID,kind,state:diagnostics[kind]}});};
  const files: CacheEntry[] = [1,2].map(id => ({key:'key'+id,stamp:'stamp'+id,bytes:1073741824,lastRequest:id===1?Date.UTC(2026,9,16,8,30):0,songs:[{id:String(id),title:'测试歌曲 '+id}],songCount:1,known:true,protected:id===2,state:'文件存在 · 按 MD5 复用'}));
  await page.route('**/api/**',async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname.slice(5), body = req.method()==='POST' ? path==='proxy/import'?{text:req.postData()}:req.postDataJSON() : {};
    calls.push({path,body,token:req.headers()['x-swd-token']});
    const event=path.match(/^network\/(connectivity|calibration)\/events$/);
    if(event||path==='block-demands/events'||path==='history/events')return route.continue();
    if (req.headers()['x-swd-token'] !== 'browser-fixture-token') return route.fulfill({status:403,body:'invalid token'});
    let data: unknown = {ok:true};
    if (path==='status') data = state;
    if(path==='import/dismiss-suggestion')state.videoImportSuggested=false;
    if(path==='exit'||path==='restart')data={ok:true,action:path};
    if(path==='storage/check')data={confirmVideoPolicy:false};
    if(path==='storage/create-database')data={path:body.databasePath};
    if(path==='storage/prepare-database')data={path:body.databasePath,created:!state.settings.databasePath};
    if(path==='path/select')data={path:body.path,canceled:!body.path};
    if(path==='terms/current')data=terms;
    if(path==='terms/accept'){terms.accepted=true;data={ok:true};}
    if(path==='vrcx/detect')data={path:'C:\\VRCX\\VRCX.sqlite3',found:false,existingSongs:0};
    if(path==='vrcx/preview')return route.fulfill({status:400,body:'未找到默认位置的 VRCX 数据库'});
    const command=path.match(/^network\/(connectivity|calibration)\/(start|cancel)$/);
    if(command){const kind=command[1] as keyof typeof diagnostics,diag=diagnostics[kind];diag.revision++;diag.running=command[2]==='start';diag.phase=diag.running?'running':'canceled';if(diag.running){diag.run++;diag.upstream=body.upstream as string;}await emit(kind);}
    if (path==='inventory') data={totalSongs:2,coveredSongs:1,coverageKnown:true,updated:'2026-10-16T00:00:00Z'};
    if (path==='downloads') data={tasks:[],bytesPerSecond:0,gets:{requests:[],total:0}};
    if (path==='songs') data={storageID:'fixture',entries:[],total:0};
    if(path==='history')data={storageID:'fixture',requests:[],hasMore:false,nextCursor:''};
    if(path==='response-sources')data={downloadBytes:0,cacheBytes:0,localBytes:0};
    if(path==='latency')data={from:Date.now()-72*3600000,to:Date.now(),availableFrom:null,partial:true,blocking:{samples:0,p50MS:null,p95MS:null},ready:{samples:0,p50MS:null,p95MS:null}};
    if (path==='requests') data={storageID:'fixture',requests:[],hasMore:false};
    if(path==='library')data={storageID:'fixture',databaseID:'fixture-db',songs:files.map(f=>({id:f.songs[0].id,title:f.songs[0].title,artist:'',dancer:'',group:'',score:1,hasDemand:true,policy:'normal',resource:f.key})),resources:Object.fromEntries(files.map(f=>[f.key,{key:f.key,size:f.bytes,duration:null,songs:f.songs,songCount:f.songCount,local:f}])),keys:files.map(f=>f.key),total:files.length,fileCount:files.length,totalBytes:files.length*1073741824};
    if (path==='cache/delete') {
      const selected = body.entries as {key:string;stamp:string}[];
      data=selected.map(item=>({key:item.key,result:'deleted'}));
      for(const item of selected) {const i=files.findIndex(f=>f.key===item.key); if(i>=0)files.splice(i,1);}
    }
    if (path==='settings/theme') {state.settings.theme=body.theme as Settings['theme'];state.activeSettings.theme=state.settings.theme;}
    if (path==='settings') {const {confirmVideoPolicy,...settings}=body;state.settings={...state.settings,...settings};state.activeSettings.restoreHostsOnStopOrExit=state.settings.restoreHostsOnStopOrExit; state.restartRequired=JSON.stringify(state.settings)!==JSON.stringify(state.activeSettings);}
    if (path==='activation/enable') {if(!state.hosts.ready){state.hosts.ready=true;state.hosts.managed=true;state.hosts.exitRestorePrepared=state.activeSettings.restoreHostsOnStopOrExit;}state.running=true;state.queue={running:state.activeSettings.queueWithGateway,desired:state.activeSettings.queueWithGateway};state.activation={phase:'waiting',readyAt:new Date().toISOString()};}
    if (path==='hosts/prepare'&&state.hosts.managed&&state.activeSettings.restoreHostsOnStopOrExit) state.hosts.exitRestorePrepared=true;
    if (path==='activation/disable') {state.running=false;state.queue={running:false,desired:false};if(state.activeSettings.restoreHostsOnStopOrExit){state.hosts.ready=false;state.hosts.managed=false;state.hosts.exitRestorePrepared=false;}state.activation={phase:'stopped'};}
    await route.fulfill({json:data,headers:{'Set-Cookie':`fixture-gate=${terms.accepted?'0':'1'}; Path=/`}});
  });
  const historyChanged=()=>page.request.post('/__fixture/events',{data:{id:fixtureID,kind:'history',state:{closed:false}}});
  return {state,calls,files,terms,diagnostics,emit,fixtureID,historyChanged};
}
