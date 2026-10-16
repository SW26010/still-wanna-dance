import type { Page } from '@playwright/test';
import type { CacheEntry, Status } from '../src/types';
export function statusFixture(): Status {
  const settings = {storageDir:'C:\\UI-Test\\storage',maxCacheBytes:40*1073741824,cacheUnlimited:true,requestRetentionDays:30,throughputIntervalMinutes:20,queuePrefetchCount:3,prepareAheadSeconds:500,downloadUpstream:'auto',upstreamMode:'direct',proxyBackend:'external-socks5',socks5Address:'',socks5Username:'',autoStartCDN:true,autoStartQueue:true,manualLogDir:false,logDir:'C:\\UI-Test\\logs'};
  return {running:false,settings:{...settings},activeSettings:{...settings},restartRequired:false,hosts:{ready:false,message:'尚未接入'},portOK:true,httpsPortOK:true,batch:{},lastBatch:{},import:{},queue:{},defaultLogDir:settings.logDir,defaultStorageDir:settings.storageDir,portableDir:'C:\\UI-Test',setupSettingsVersion:'1',activation:{},upstreamMonitor:{results:[]}};
}
export async function mockAPI(page: Page) {
  const fixtureID=crypto.randomUUID();
  await page.context().addCookies([{name:'fixture-id',value:fixtureID,url:'http://127.0.0.1:18473'}]);
  const state = statusFixture(), calls: {path:string;body:Record<string,unknown>;token?:string}[] = [];
  const terms={text:'测试完整条款。软件许可不等于内容授权。',version:'fixture',sha256:'fixture-hash',accepted:true,needsConfirmation:false};
  const diagnostics={connectivity:{revision:1,run:0,upstream:'direct',phase:'idle',running:false,results:[],lastValid:{}},calibration:{revision:1,run:0,upstream:'direct',phase:'idle',running:false,lastValid:{},referenceBitrateBPS:2000000}};
  const emit=async(kind:keyof typeof diagnostics)=>{await page.request.post('/__fixture/events',{data:{id:fixtureID,kind,state:diagnostics[kind]}});};
  const files: CacheEntry[] = [1,2].map(id => ({key:'key'+id,stamp:'stamp'+id,bytes:1073741824,lastRequest:id===1?Date.UTC(2026,9,16,8,30):0,songs:[{id:String(id),title:'测试歌曲 '+id,current:true}],songCount:1,known:true,protected:id===2,state:'已缓存'}));
  await page.route('**/api/**',async route => {
    const req = route.request(), url = new URL(req.url()), path = url.pathname.slice(5), body = req.method()==='POST' ? path==='proxy/import'?{text:req.postData()}:req.postDataJSON() : {};
    calls.push({path,body,token:req.headers()['x-swd-token']});
    const event=path.match(/^setup\/network\/(connectivity|calibration)\/events$/);
    if(event)return route.continue();
    if (req.headers()['x-swd-token'] !== 'browser-fixture-token') return route.fulfill({status:403,body:'invalid token'});
    let data: unknown = {ok:true};
    if (path==='status') data = state;
    if(path==='terms/current')data=terms;
    if(path==='terms/accept'){terms.accepted=true;data={ok:true};}
    if(path==='setup')data={schemaVersion:1,step:'welcome',state:'completed'};
    if(path==='setup/storage')data={space:{state:'ok'},writable:{state:'ok'},library:{state:'ok'},freeBytes:100*2**30,totalBytes:200*2**30};
    if(path==='setup/logs')data={state:'ok',directory:state.defaultLogDir,message:'日志可读取'};
    if(path==='setup/folder')data={path:body.kind==='storage'?'D:\\UI-Test\\cache':'D:\\UI-Test\\videos',canceled:false};
    if(path==='setup/apply'){state.setupApplication={id:body.id as string,phase:'completed',configSaved:true,importStarted:!!body.importSource};data=state.setupApplication;}
    if(path==='setup/receipt')data=state.setupApplication || {id:body.id,phase:'missing',error:'未找到原回执'};
    const command=path.match(/^setup\/network\/(connectivity|calibration)\/(start|cancel)$/);
    if(command){const kind=command[1] as keyof typeof diagnostics,diag=diagnostics[kind];diag.revision++;diag.running=command[2]==='start';diag.phase=diag.running?'running':'canceled';if(diag.running){diag.run++;diag.upstream=body.upstream as string;}await emit(kind);}
    if (path==='inventory') data={totalSongs:2,coveredSongs:1,coverageKnown:true,updated:'2026-10-16T00:00:00Z'};
    if (path==='downloads') data={tasks:[],bytesPerSecond:0};
    if (path==='requests') data={storageID:'fixture',requests:[],hasMore:false};
    if (path==='cache') data={storageID:'fixture',entries:files,total:files.length,fileCount:files.length,totalBytes:files.length*1073741824};
    if (path==='cache/delete') {
      const selected = body.entries as {key:string;stamp:string}[];
      data=selected.map(item=>({key:item.key,result:'deleted'}));
      for(const item of selected) {const i=files.findIndex(f=>f.key===item.key); if(i>=0)files.splice(i,1);}
    }
    if (path==='settings') {state.settings={...state.settings,...body}; state.restartRequired=JSON.stringify(state.settings)!==JSON.stringify(state.activeSettings);}
    if (path==='start') state.running=true;
    if (path==='stop') state.running=false;
    await route.fulfill({json:data});
  });
  return {state,calls,files,terms,diagnostics,emit};
}
