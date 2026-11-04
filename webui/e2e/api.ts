import type { Page } from '@playwright/test';
import type { CacheEntry, Settings, Status } from '../src/types';
export async function choosePath(page: Page, label: string, path: string) {
  await page.route('**/api/path/select', route => route.fulfill({json:{path,canceled:false}}), {times:1});
  const response = page.waitForResponse('**/api/path/select');
  await page.getByRole('textbox',{name:label,exact:true}).click();
  await response;
}
export function statusFixture(): Status {
  const settings: Settings = {theme:'system',logLevel:'info',retainFavorites:true,databasePath:'C:\\UI-Test\\storage\\storage.sqlite',videoDir:'D:\\Videos',blockCacheDir:'',videoBudgetBytes:40*1073741824,videoStorageMode:'unlimited',aggressiveBlockReclamation:'auto',requestRetentionMode:'unlimited',requestRetentionDays:0,throughputIntervalMinutes:20,prepareAheadSeconds:500,downloadConcurrencySoftLimit:16,downloadGrowIntervalMS:250,localProtectionGraceSeconds:300,upstreamMode:'direct',proxyBackend:'external-socks5',socks5Address:'',socks5Username:'',autoAccelerate:false,autoOpenWebUI:true,exitWithVRChat:false,queueWithGateway:true,restoreHostsOnStopOrExit:true,vrchatLogDir:''};
  return {vrcxImportAvailable:true,videoImportSuggested:false,running:false,settings:{...settings},activeSettings:{...settings},restartRequired:false,hosts:{ready:false,message:'尚未接入'},portOK:true,httpsPortOK:true,batch:{},lastBatch:{},import:{},queue:{},defaultLogDir:'C:\\UI-Test\\logs',defaultDatabasePath:settings.databasePath,defaultVideoDir:settings.videoDir,defaultBlockCacheDir:'C:\\UI-Test\\work',portableDir:'C:\\UI-Test',activation:{},upstreamMonitor:{results:[]},catalogSync:{initializing:false,checking:false}};
}
export async function mockAPI(page: Page) {
  await page.routeWebSocket('**/api/lifecycle',socket=>socket.send(JSON.stringify({action:'running'})));
  const fixtureID=crypto.randomUUID();
  await page.context().addCookies([{name:'fixture-id',value:fixtureID,url:'http://127.0.0.1:18473'}]);
  const state = statusFixture(), calls: {path:string;body:Record<string,unknown>;token?:string}[] = [];
  const terms={text:'测试完整条款。软件许可不等于内容授权。',version:'fixture',sha256:'fixture-hash',accepted:true,needsConfirmation:false};
  const diagnostics={connectivity:{revision:1,run:0,upstream:'direct',phase:'idle',running:false,results:[],lastResult:undefined},calibration:{revision:1,run:0,upstream:'direct',phase:'idle',running:false,lastValid:undefined,referenceBitrateBPS:2000000}};
  const emit=async(kind:keyof typeof diagnostics)=>{await page.request.post('/__fixture/events',{data:{id:fixtureID,kind,state:diagnostics[kind]}});};
  const files: CacheEntry[] = [1,2].map(id => ({key:'key'+id,stamp:'stamp'+id,bytes:1073741824,lastRequest:id===1?Date.UTC(2026,9,16,8,30):0,songs:[{id:String(id),title:'测试歌曲 '+id}],songCount:1,known:true,protected:id===2,state:'文件存在 · 按 MD5 复用'}));
  await page.route('**/api/**',async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname.slice(5), method=req.method();
    let body:Record<string,unknown>={},invalidJSON=false;
    if(method==='POST') {
      try { body=path==='proxy/import'?{text:req.postData()}:req.postDataJSON()||{}; }
      catch { invalidJSON=true; }
    }
    calls.push({path,body,token:req.headers()['x-swd-token']});
    const event=path.match(/^network\/(connectivity|calibration)\/events$/);
    if(method==='GET'&&(event||path==='block-demands/events'||path==='history/events'))return route.continue();
    if (req.headers()['x-swd-token'] !== 'browser-fixture-token') return route.fulfill({status:403,body:'invalid token'});
    let handled=false;
    // Track a recognized route even when malformed JSON prevents its handler from running.
    const match=(...keys:string[])=>{
      if(!keys.includes(`${method} ${path}`))return false;
      handled=true;
      return !invalidJSON;
    };
    let data: unknown = {ok:true};
    if (match('GET status')) data = state;
    if(match('POST import/dismiss-suggestion'))state.videoImportSuggested=false;
    if(match('GET import/space'))data={mode:'unknown',videoDir:state.import.videoDir||state.activeSettings.videoDir};
    if(match('POST exit','POST restart'))data={ok:true,action:path};
    if(match('POST storage/check'))data={confirmVideoPolicy:false};
    if(match('POST storage/validate-location'))data={valid:true};
    if(match('POST storage/create-database'))data={path:body.databasePath};
    if(match('POST storage/prepare-database'))data={path:body.databasePath,created:!state.settings.databasePath};
    if(match('POST path/select'))data={path:body.path,canceled:!body.path};
    if(match('GET terms/current'))data=terms;
    if(match('POST terms/accept'))terms.accepted=true;
    if(match('POST vrcx/detect'))data={path:'C:\\VRCX\\VRCX.sqlite3',found:false,existingSongs:0};
    if(match('POST vrcx/preview'))return route.fulfill({status:400,body:'未找到默认位置的 VRCX 数据库'});
    const command=path.match(/^network\/(connectivity|calibration)\/(start|cancel)$/);
    if(command&&match(`POST ${path}`)){const kind=command[1] as keyof typeof diagnostics,diag=diagnostics[kind];diag.revision++;diag.running=command[2]==='start';diag.phase=diag.running?'running':'canceled';if(diag.running){diag.run++;diag.upstream=state.activeSettings.upstreamMode;if(kind==='connectivity')Object.assign(diag,{operation:body.operation});}await emit(kind);}
    if (match('GET inventory')) data={totalSongs:2,coveredSongs:1,coverageKnown:true,updated:'2026-10-16T00:00:00Z'};
    if (match('GET downloads')) data={tasks:[],bytesPerSecond:0,gets:{requests:[],total:0}};
    if (match('GET songs')) data={storageID:'fixture',entries:[],total:0};
    if(match('GET history'))data={storageID:'fixture',requests:[],hasMore:false,nextCursor:''};
    if(match('GET response-sources'))data={downloadBytes:0,cacheBytes:0,localBytes:0};
    if(match('GET latency'))data={from:Date.now()-72*3600000,to:Date.now(),blocking:{samples:0,p50MS:null,p95MS:null},ready:{samples:0,p50MS:null,p95MS:null}};
    if (match('GET requests')) data={storageID:'fixture',requests:[],hasMore:false};
    if(match('GET library'))data={storageID:'fixture',databaseID:'fixture-db',songs:files.map(f=>({id:f.songs[0].id,title:f.songs[0].title,artist:'',dancer:'',group:'',score:1,hasDemand:true,policy:'normal',resource:f.key})),resources:Object.fromEntries(files.map(f=>[f.key,{key:f.key,size:f.bytes,duration:null,songs:f.songs,songCount:f.songCount,local:f}])),keys:files.map(f=>f.key),total:files.length,fileCount:files.length,totalBytes:files.length*1073741824};
    if (match('POST cache/delete')) {
      const selected = body.entries as {key:string;stamp:string}[];
      data=selected.map(item=>({key:item.key,result:'deleted'}));
      for(const item of selected) {const i=files.findIndex(f=>f.key===item.key); if(i>=0)files.splice(i,1);}
    }
    if (match('POST settings/theme')) {state.settings.theme=body.theme as Settings['theme'];state.activeSettings.theme=state.settings.theme;}
    if (match('POST settings')) {
      // Match /api/status: request-only fields are omitted.
      const {confirmVideoPolicy,proxyImportID,socks5Password,...settings}=body;
      state.settings={...state.settings,...settings};
      state.activeSettings.restoreHostsOnStopOrExit=state.settings.restoreHostsOnStopOrExit;
      state.restartRequired=JSON.stringify(state.settings)!==JSON.stringify(state.activeSettings);
    }
    if (match('POST activation/enable')) {if(!state.hosts.ready){state.hosts.ready=true;state.hosts.managed=true;state.hosts.exitRestorePrepared=state.activeSettings.restoreHostsOnStopOrExit;}state.running=true;state.queue={running:state.activeSettings.queueWithGateway,desired:state.activeSettings.queueWithGateway};state.activation={phase:'waiting',readyAt:new Date().toISOString()};}
    if (match('POST hosts/prepare')&&state.hosts.managed&&state.activeSettings.restoreHostsOnStopOrExit) state.hosts.exitRestorePrepared=true;
    if (match('POST activation/disable')) {state.running=false;state.queue={running:false,desired:false};if(state.activeSettings.restoreHostsOnStopOrExit){state.hosts.ready=false;state.hosts.managed=false;state.hosts.exitRestorePrepared=false;}state.activation={phase:'stopped'};}
    // These actions only acknowledge the request; tests needing progress provide their own route.
    match('POST cache/open','POST songs','POST inventory/scan',
      'POST batch/scan','POST batch/start','POST batch/download','POST batch/cancel',
      'POST import/start','POST import/cleanup','POST import/pause','POST import/resume','POST import/discard',
      'POST upstream/check','POST upstream/check/catalog','POST upstream/check/playback','POST upstream/check/latency','POST upstream/check/throughput',
      'POST proxy/import/discard','POST vrcx/dismiss','POST vrcx/discard','POST terms/leave','POST terms/return');
    if(!handled)return route.fulfill({status:404,body:'not found'});
    if(invalidJSON)return route.fulfill({status:400,body:'invalid JSON'});
    await route.fulfill({json:data,headers:{'Set-Cookie':`fixture-gate=${terms.accepted?'0':'1'}; Path=/`}});
  });
  const historyChanged=()=>page.request.post('/__fixture/events',{data:{id:fixtureID,kind:'history',state:{closed:false}}});
  return {state,calls,files,terms,diagnostics,emit,fixtureID,historyChanged};
}
