import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
const script = readFileSync(new URL('../internal/console/assets/setup.js',import.meta.url),'utf8');
const consoleScript = readFileSync(new URL('../internal/console/assets/console.js',import.meta.url),'utf8');
const controls = consoleScript.slice(consoleScript.indexOf('function renderControls()'),consoleScript.indexOf('function renderQueue('));
const html = ['index.html','setup.html'].map(f=>readFileSync(new URL('../internal/console/'+f,import.meta.url),'utf8')).join('\n');
const buttons = [...html.matchAll(/<button\b[^>]*\bid="([^"]+)"/g)].map(m=>m[1]);
const flush = ()=>new Promise(resolve=>setImmediate(resolve));
test('calibration reads the current proxy draft without saving or changing routing',async()=>{
  const s=setup({step:'proxy'});await flush();
  s.node('socks5Address').value='127.0.0.1:7891';
  s.node('socks5Username').value='fixture-user';
  s.node('socks5Password').value='';
  const count=s.calls.length,mode=s.node('upstreamMode').value;
  const draft=s.window.swdSetupCalibrationPlan();
  assert.equal(draft.socks5Address,'127.0.0.1:7891');
  assert.equal(draft.baseVersion,'v1');assert.equal(draft.page,'proxy');assert.equal(draft.upstreamMode,'proxy');
  assert.equal(Object.hasOwn(draft,'socks5Password'),false);
  assert.equal(s.calls.length,count);assert.equal(s.node('upstreamMode').value,mode);
  s.node('socks5Password').value='new-fixture-secret';
  assert.equal(s.window.swdSetupCalibrationPlan().socks5Password,'new-fixture-secret');
});
function deferred(){let resolve;const promise=new Promise(r=>resolve=r);return {resolve,promise};}
function setup({step='welcome',request=async()=>({}),stateOverride={},receipt={accepted:true},search='',reconnect=async()=>({state:'reloading'}),confirm=()=>true,applicationID='',firstEntry=false,sharedImport=false}={}) {
  const nodes=new Map(),events={},windowEvents={},calls=[];
  const node=id=>{
    if(!nodes.has(id))nodes.set(id,{id,value:'',checked:false,hidden:false,dataset:{},listeners:{},
      addEventListener(name,fn){(this.listeners[name] ||= []).push(fn);},
      dispatchEvent(e){for(const f of this.listeners[e.type]||[])f(e);},
      setAttribute(k,v){this[k]=v;},removeAttribute(k){delete this[k];},getAttribute(k){return this[k]||null;},
      setCustomValidity(v){this.validationMessage=v;},checkValidity(){return !this.validationMessage && !(this.required&&!this.value);},
      reportValidity(){this.reported=true;},focus(){this.focused=true;},
      showModal(){this.open=true;},close(){this.open=false;},
      closest(selector){return selector==='[data-setup-settings]'?this.group:null;},
      querySelectorAll(){return [];},before(marker){marker.parentNode=this.parentNode;},after(child){child.parentNode=this.parentNode;},
      append(...children){for(const child of children)child.parentNode=this;},
      replaceChildren(){},
    });return nodes.get(id);
  };
  const groups=['storage','proxy'].map(name=>{const n=node(name+'Group');n.dataset.setupSettings=name;return n;});
  const ids=['storageDir','maxCacheGiB','upstreamMode','proxyBackend','proxyImportID','proxyImportCount','proxyImportText','socks5Address','socks5Username','socks5Password','setupCustomStorage','setupDirect','setupSocks5'];
  for(const id of ids)node(id).group=groups[['storageDir','maxCacheGiB','setupCustomStorage'].includes(id)?0:1];
  node('settings').querySelectorAll=()=>ids.map(node);
  node('preferences').parentNode=node('page-settings');
  const window={location:{hash:'#/setup',search,pathname:'/',assign(url){this.href=url;}},history:{replaceState(_,__,url){calls.push({path:'replaceURL',url});}},confirm:message=>{calls.push({path:'confirm',message});return confirm();},swdInitialRoute:'#/setup',addEventListener(name,fn){windowEvents[name]=fn;}};
  const memory=new Map(applicationID?[['swd-setup-application',applicationID]]:[]);
  window.swdTermsGate = firstEntry ? {step,pageID:'test-page'} : null;
  const state={settings:{storageDir:'C:\\Portable\\data\\storage',maxCacheBytes:0,upstreamMode:'direct',socks5Address:'',socks5Username:''},defaultStorageDir:'C:\\Portable\\data\\storage',portableDir:'C:\\Portable',hosts:{ready:false},queue:{},batch:{},setupSettingsVersion:'v1',...stateOverride};
  const context={window,globalThis:window,document:{
    createComment:node,querySelectorAll:s=>s==='button'?buttons.map(node):s==='[data-setup-settings]'?groups:[],
    addEventListener(n,f){events[n]=f;},dispatchEvent(){}},
    $:node,settingsDirty:false,settingsBaseline:'original',lastState:firstEntry?null:state,connected:!firstEntry,busy:false,exiting:false,lastInventory:null,
    token:'fixture',AbortSignal,AbortController,Event,URLSearchParams,CustomEvent:class{},crypto:{randomUUID:()=> 'application-1'},
    sessionStorage:{getItem:k=>memory.get(k),setItem:(k,v)=>memory.set(k,v),removeItem:k=>memory.delete(k)},
    activationPending:()=>false,healthImpact:()=>'',renderStableList(){},
    renderSettings(s){if(window.swdSetupOwnsSettings?.())return;for(const [id,key]of [['storageDir','storageDir'],['upstreamMode','upstreamMode'],['socks5Address','socks5Address'],['socks5Username','socks5Username']])node(id).value=s.settings[key]||'';node('maxCacheGiB').value=String((s.settings.maxCacheBytes||0)/1073741824);node('socks5Password').value='';window.swdSetupSettingsLoaded?.(s);context.settingsBaseline='wizard';},
    renderControls(){window.swdSetupRenderControls?.();},setText:(id,value)=>{node(id).textContent=value;},
    requestRefresh:async()=>{if(firstEntry){calls.push({path:'/api/status'});const data=await request('/api/status');context.connected=!data.error;if(!data.error)context.lastState=state;}events.consolestate?.();},reconnectAfterRestart:async()=>{calls.push({path:'reconnect'});return reconnect();},
    fetch:async(path,options={})=>{
      const body=options.body?(path==='/api/proxy/import'?options.body:JSON.parse(options.body)):null;calls.push({path,body});
      if(path==='/api/setup'&&!options.method)return {ok:true,json:async()=>({step,state:'active'})};
      const data=await request(path,body);
      if(path==='/api/setup/storage')return {ok:!data.error,json:async()=>({space:{state:'ok'},writable:{state:'ok',message:'可写'},library:{state:'ok',message:'新库'},freeBytes:1,totalBytes:2,...data})};
      if(path==='/api/terms/accept'&&!data.error)receipt.accepted=true;
      return {ok:data.status ? data.status < 400 : !data.error,status:data.status || (data.error?400:200),json:async()=>path==='/api/terms/current'?{text:'完整条款',version:'v1',sha256:'hash1',accepted:receipt.accepted,...data}:data,text:async()=>data.error||'完整条款'};
    },
  };
  if(sharedImport){
    context.renderImport=()=>{};
    context.renderControls=()=>{window.swdRenderProxyControls?.();window.swdSetupRenderControls?.();};
    context.settingsFingerprint=()=>JSON.stringify(ids.map(id=>[node(id).value,node(id).checked]));
    context.notice=()=>{};
    context.action=async(path,body)=>{calls.push({path:'/api/'+path,body});return true;};
    vm.runInNewContext(consoleScript.slice(consoleScript.indexOf('let proxyImportGeneration'),consoleScript.indexOf('function renderControls()')),context);
    vm.runInNewContext(consoleScript.slice(consoleScript.indexOf('async function saveSettings()'),consoleScript.indexOf("$('settings').addEventListener('submit'")),context);
    node('settings').addEventListener('input',()=>{context.settingsDirty=context.settingsFingerprint()!==context.settingsBaseline;context.renderControls();});
  }
  vm.runInNewContext(script,context);
  return {node,window,context,events,windowEvents,calls,memory,
    go:name=>node('setupStep'+name[0].toUpperCase()+name.slice(1)).onclick(),
    emit:(id,event)=>node(id).dispatchEvent(new Event(event)),
  };
}
const business=s=>s.calls.filter(c=>['/api/settings','/api/setup/apply','/api/import/start','/api/activation/enable'].includes(c.path));

for(const source of ['request','file'])test(`leaving setup invalidates a pending node ${source} before restoring saved settings`,async()=>{
  const pending=deferred();
  const s=setup({step:'proxy',sharedImport:true,request:path=>path==='/api/proxy/import'?pending.promise:Promise.resolve({})});await flush();
  const original=s.context.lastState.settings;
  s.node('storageDir').value='D:\\abandoned';s.node('upstreamMode').value='proxy';s.node('proxyBackend').value='mihomo';
  if(source==='request'){
    s.node('proxyImportText').value='synthetic node';s.emit('settings','input');s.emit('proxyImport','click');
  }else{
    s.node('proxyImportFile').files=[{size:10,text:()=>pending.promise}];s.emit('proxyImportFile','change');
  }
  assert.equal(s.window.swdProxyImportPending(),true);
  s.node('setupLeave').onclick();
  assert.equal(s.window.swdProxyImportPending(),false);
  assert.equal(s.node('storageDir').value,original.storageDir);
  assert.equal(s.node('upstreamMode').value,original.upstreamMode);
  assert.equal(s.node('proxyBackend').value,'external-socks5');
  pending.resolve(source==='request'?{id:'abandoned-node-draft',accepted:1,items:[]}:'abandoned node file');await flush();
  const released=s.calls.filter(c=>c.path==='/api/proxy/import/discard');
  assert.equal(released.length,source==='request'?1:0);
  if(source==='request')assert.equal(released[0].body.id,'abandoned-node-draft');
  assert.equal(s.node('proxyImportID').value,'');assert.equal(s.node('proxyImportText').value,'');
  assert.equal(s.context.settingsDirty,false);
  s.node('requestRetentionDays').value='7';s.context.settingsDirty=true;
  await s.context.saveSettings();
  const saved=s.calls.find(c=>c.path==='/api/settings').body;
  assert.equal(saved.storageDir,original.storageDir);assert.equal(saved.upstreamMode,original.upstreamMode);
  assert.equal(saved.proxyBackend,'external-socks5');assert.equal(saved.proxyImportID,'');assert.equal(saved.requestRetentionDays,7);
});

test('wizard stages Mihomo draft through the shared final save and omits raw nodes',async()=>{
  const s=setup({step:'proxy'});await flush();
  s.node('upstreamMode').value='proxy';s.node('proxyBackend').value='mihomo';s.node('proxyImportID').value='opaque';s.node('proxyImportCount').value='6';
  s.events.consolestate();await s.go('finish');
  assert.match(s.node('setupSummary').textContent,/内嵌 Mihomo/);assert.match(s.node('setupSummary').textContent,/替换.*6 个/);
  assert.equal(business(s).length,0);await s.node('setupNext').onclick();
  const applied=business(s).find(c=>c.path==='/api/setup/apply');assert.equal(applied.body.proxyImportID,'opaque');assert.equal(applied.body.proxyBackend,'mihomo');
  assert.equal(applied.body.proxyImportText,undefined);
});

test('wizard cannot apply while a node preview is pending',async()=>{
  const s=setup({step:'finish'});await flush();
  s.window.swdProxyImportPending=()=>true;s.context.renderControls();
  assert.equal(s.node('setupNext').disabled,true);assert.equal(s.node('storageDir').disabled,true);
  s.window.swdProxyImportPending=()=>false;s.context.renderControls();assert.equal(s.node('setupNext').disabled,false);
});

test('checking import never opens a picker',async()=>{
  const s=setup({step:'storage',stateOverride:{setupNetwork:{monitor:{results:[{operation:'catalog',state:'available',estimatedLatencyMS:24},{operation:'playback_url',state:'available',estimatedLatencyMS:12,validUntil:'2000-01-01T00:00:00Z'}]}}},request:async path=>path==='/api/setup/folder'?{canceled:true}:{}});await flush();
  assert.equal(s.node('setupImportFolder').hidden,true);assert.equal(s.node('setupImportDetails').hidden,true);
  s.node('setupImportEnabled').checked=true;await s.node('setupImportEnabled').onchange();
  assert.equal(s.node('setupImportFolder').hidden,false);assert.equal(s.node('setupImportDetails').hidden,false);
  assert.equal(s.calls.some(c=>c.path==='/api/setup/folder'),false);
  await s.node('setupImportSelection').onclick();
  assert.equal(s.node('setupImportEnabled').checked,true);assert.equal(s.node('setupImportSource').value,'');assert.equal(business(s).length,0);
  await s.go('proxy');assert.equal(s.node('setupTitle').textContent,'存储');assert.match(s.node('setupImportFeedbackText').textContent,/请选择要导入/);assert.equal(s.node('setupImportFeedback').hidden,false);assert.equal(s.node('setupImportSelection').getAttribute('aria-invalid'),'true');assert.equal(s.node('setupMessage').textContent,'');
  s.node('setupImportEnabled').checked=false;s.node('setupImportEnabled').onchange();assert.equal(s.node('setupImportFolder').hidden,true);assert.equal(s.node('setupImportDetails').hidden,true);
  await s.go('proxy');assert.equal(s.node('setupTitle').textContent,'网络');
});

for(const [old,title]of [['terms','开始'],['import','存储'],['direct','网络'],['environment','启用']])test('legacy '+old+' progress resumes at the merged step',async()=>{
  const s=setup({step:old});await flush();assert.equal(s.node('setupTitle').textContent,title);assert.equal(business(s).length,0);
});

test('folder selection is staged, canceled selection preserves values and stale results cannot revive drafts',async()=>{
  let result={path:'D:\\media',canceled:false};
  const s=setup({step:'storage',request:async path=>path==='/api/setup/folder'?result:{}});await flush();
  await s.node('setupStoragePath').onclick();assert.equal(s.node('storageDir').value,'D:\\media');assert.equal(s.node('setupCustomStorage').checked,true);
  result={path:'',canceled:true};await s.node('setupStoragePath').onclick();assert.equal(s.node('storageDir').value,'D:\\media');
  result={path:'D:\\videos',canceled:false};s.node('setupImportEnabled').checked=true;await s.node('setupImportSelection').onclick();assert.equal(s.node('setupImportSource').value,'D:\\videos');assert.equal(s.node('setupImportSelection').value,'D:\\videos');
  s.node('setupImportEnabled').checked=false;s.node('setupImportEnabled').onchange();assert.equal(s.node('setupImportSource').value,'');assert.equal(business(s).length,0);
  const d=deferred();result=d.promise;const picking=s.node('setupStoragePath').onclick();await flush();
  s.node('setupLeave').onclick();d.resolve({path:'D:\\late'});await picking;
  assert.equal(s.node('storageDir').value,s.context.lastState.settings.storageDir);assert.equal(business(s).length,0);
});

test('folder request and failure use field feedback without changing footer messages',async()=>{
  let result=deferred();
  const s=setup({step:'storage',request:async path=>path==='/api/setup/folder'?result.promise:{}});await flush();
  s.node('setupMessage').textContent='其他提示';
  let pending=s.node('setupStoragePath').onclick();await flush();
  assert.equal(s.node('setupMessage').textContent,'其他提示');assert.equal(s.node('setupStoragePath').getAttribute('aria-busy'),'true');
  result.resolve({error:'窗口未能打开'});await pending;
  assert.equal(s.node('setupMessage').textContent,'其他提示');assert.equal(s.node('setupStorageFeedback').hidden,false);assert.match(s.node('setupStorageFeedbackText').textContent,/窗口未能打开/);
  result=deferred();pending=s.node('setupStoragePath').onclick();await flush();assert.equal(s.node('setupStorageFeedback').hidden,true);
  result.resolve({canceled:true});await pending;assert.equal(s.node('setupMessage').textContent,'其他提示');
  s.node('setupImportEnabled').checked=true;s.node('setupImportEnabled').onchange();await s.go('proxy');assert.equal(s.node('setupImportFeedback').hidden,false);
  s.node('setupImportEnabled').checked=false;s.node('setupImportEnabled').onchange();assert.equal(s.node('setupImportFeedback').hidden,true);assert.equal(s.node('setupImportSelection').getAttribute('aria-invalid'),'false');
});

test('storage reset keeps displayed results during one coalesced directory check',async()=>{
  let hold=false;const d=deferred();
  const s=setup({step:'storage',request:async path=>path==='/api/setup/storage'&&hold?d.promise:{}});await flush();
  const space=s.node('setupDiskSpace').textContent;
  s.node('maxCacheGiB').value='25';s.events.consolestate();hold=true;
  const before=s.calls.filter(c=>c.path==='/api/setup/storage').length;
  s.node('setupReset').onclick();await flush();s.events.consolestate();await s.node('setupCheckStorage').onclick();
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/storage').length,before+1);
  assert.equal(s.node('setupDiskSpace').textContent,space);assert.equal(s.node('setupStorageStatus').textContent,'');assert.equal(s.node('setupMessage').textContent,'');assert.equal(s.node('setupCheckStorage').hidden,true);
  d.resolve({});await flush();assert.equal(s.node('setupDiskSpace').textContent,space);assert.equal(s.node('setupStorageStatus').textContent,'');
});

test('capacity choice maps unlimited to zero and rejects a non-positive limit before leaving',async()=>{
  const s=setup({step:'storage'});await flush();assert.equal(s.node('setupCapacityUnlimited').checked,true);
  s.node('setupCapacityUnlimited').checked=false;s.node('setupCapacityLimited').checked=true;s.node('setupCapacityValue').value='0';s.emit('setupCapacityLimited','input');
  await s.go('proxy');assert.equal(s.node('setupTitle').textContent,'存储');assert.equal(s.node('setupCapacityValue').reported,true);
  s.node('setupCapacityValue').value='25';s.emit('setupCapacityValue','input');await s.go('finish');assert.match(s.node('setupSummary').textContent,/25 GiB/);
  await s.go('storage');s.node('setupCapacityLimited').checked=false;s.node('setupCapacityUnlimited').checked=true;s.emit('setupCapacityUnlimited','input');await s.go('finish');
  assert.match(s.node('setupSummary').textContent,/缓存空间上限：不限/);assert.equal(s.node('maxCacheGiB').value,'0');
});

test('game access checks remain visible for normal and abnormal ports or logs',async()=>{
  const s=setup({step:'finish',stateOverride:{portOK:true,httpsPortOK:true},request:async path=>path==='/api/setup/logs'?{state:'ok',message:'可读'}:{}});await flush();
  assert.equal(s.node('setupAccessCheck').textContent,'接入检查通过');assert.ok(!s.node('setupAccessDetails').hidden);
  s.context.lastState.portOK=false;s.events.consolestate();assert.match(s.node('setupAccessCheck').textContent,/关闭占用程序/);assert.match(s.node('setupPort80').textContent,/占用/);
  s.context.lastState.portOK=true;s.events.consolestate();assert.equal(s.node('setupAccessCheck').textContent,'接入检查通过');assert.ok(!s.node('setupAccessDetails').hidden);
});

test('a first-entry document with an existing receipt recovers to completion without reapplying',async()=>{
  const s=setup({firstEntry:true,applicationID:'old-apply',stateOverride:{setupApplication:{id:'old-apply',phase:'completed',configSaved:true}}});await flush();await flush();
  assert.equal(s.window.location.hash,'#/home');assert.equal(s.memory.has('swd-setup-application'),false);
  assert.equal(business(s).length,0);
});

for (const ready of [false,true]) test('one confirmation returns to the console without separate access/service requests '+ready,async()=>{
  const result=deferred();
  const s=setup({step:'finish',stateOverride:{hosts:{ready},running:ready},request:async path=>path==='/api/setup/apply'?result.promise:{}});await flush();
  const applying=s.node('setupNext').onclick();await flush();await s.node('setupNext').onclick();
  assert.equal(s.window.location.hash,'#/setup');assert.equal(s.node('setupNext').disabled,true);
  result.resolve({id:'application-1',phase:'completed',configSaved:true});await applying;await flush();
  assert.equal(s.window.location.hash,'#/home');assert.equal(s.memory.has('swd-setup-application'),false);
  s.events.consolestate();await flush();
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/apply').length,1);
  assert.equal(s.calls.filter(c=>c.path==='/api/setup'&&c.body?.state==='completed').length,1);
  assert.equal(s.calls.some(c=>['/api/hosts/enable','/api/hosts/disable','/api/start','/api/activation/enable'].includes(c.path)),false);
  assert.equal(s.context.lastState.hosts.ready,ready);assert.equal(s.context.lastState.running,ready);
});

test('failed application remains visible instead of returning as if it completed',async()=>{
  const s=setup({step:'finish',request:async(path,body)=>path==='/api/setup/apply'?{status:200,id:body.id,phase:'failed',configSaved:true,error:'导入未启动，请核对目录'}:{}});await flush();
  await s.node('setupNext').onclick();assert.equal(s.window.location.hash,'#/setup');
  assert.match(s.node('setupApplyResult').textContent,/导入未启动/);
  assert.equal(s.node('setupNext').textContent,'返回控制台');
});

test('network advice asks for missing proxy configuration before suggesting continuation',async()=>{
  const s=setup({step:'proxy'});await flush();
  s.node('setupSocks5').checked=true;s.node('socks5Address').required=true;s.emit('setupSocks5','input');s.context.renderControls();s.events.consolestate();
  assert.match(s.node('setupDirectAdvice').textContent,/先.*填写.*代理地址/);
  await s.go('finish');assert.equal(s.node('setupTitle').textContent,'网络');assert.equal(s.node('socks5Address').reported,true);
  s.node('socks5Address').value='127.0.0.1:1080';s.events.consolestate();
  assert.equal(s.node('setupDirectAdvice').hidden,true);

  s.node('upstreamMode').value='';s.events.consolestate();assert.match(s.node('setupDirectAdvice').textContent,/选择直连或代理/);
});

test('review only warns about interruption for settings changes, and explains import lifetime',async()=>{
  const s=setup();await flush();await s.go('finish');
  assert.equal(s.node('setupApplyHint').textContent,'');assert.equal(s.node('setupSaveState').textContent,'');
  await s.go('storage');s.node('setupImportSource').value='D:\\videos';await s.go('finish');
  assert.match(s.node('setupApplyHint').textContent,/确认后开始导入/);assert.doesNotMatch(s.node('setupApplyHint').textContent,/将重启/);
  await s.go('storage');s.node('maxCacheGiB').value='25';await s.go('finish');
  assert.match(s.node('setupApplyHint').textContent,/将重启程序/);assert.match(s.node('setupApplyHint').textContent,/重启后开始导入/);
  assert.match(s.node('setupSummary').textContent,/不限 → 25 GiB/);
});

test('first entry confirms in the same mounted page and initializes settings only after consent',async()=>{
  const s=setup({firstEntry:true,receipt:{accepted:false}});await flush();
  assert.equal(s.node('setupTitle').textContent,'开始');
  assert.equal(s.node('setupStepStorage').dataset.status,'unvisited');
  await s.go('welcome');await flush();await s.go('welcome');await s.go('welcome');await flush();
  assert.ok(s.calls.every(c=>c.path==='/api/terms/current'));
  let prevented=false;s.windowEvents.beforeunload({preventDefault(){prevented=true;}});assert.equal(prevented,false);
  const body=s.node('setupTermsText');body.scrollTop=123;
  await s.node('setupAcceptTerms').onclick();await flush();
  assert.equal(s.node('setupTermsText'),body);assert.equal(body.scrollTop,123);
  assert.equal(s.window.location.hash,'#/setup');assert.equal(s.window.location.href,undefined);
  assert.equal(s.node('setupTitle').textContent,'开始');
  assert.equal(s.node('setupTermsLabel').textContent,'阅读并同意');assert.equal(s.node('setupAcceptTerms').disabled,true);
  assert.equal(s.node('setupStepWelcome').dataset.status,'changed');
  assert.equal(s.node('setupNext').disabled,false);assert.equal(s.window.swdTermsGate,null);
  assert.equal(s.calls.filter(c=>c.path==='/api/terms/accept').length,1);
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/network').length,0);
  assert.equal(business(s).length,0);
  await s.go('storage');assert.equal(s.node('storageDir').value,s.context.lastState.settings.storageDir);
});

test('first-entry presence and safe exit remain available without business state',async()=>{
  const s=setup({firstEntry:true,receipt:{accepted:false}});await flush();
  s.windowEvents.pageshow({persisted:false});s.windowEvents.pagehide();s.windowEvents.pageshow({persisted:true});
  assert.deepEqual(s.calls.filter(c=>c.path.startsWith('/api/terms/')&&c.path!=='/api/terms/current'),[
    {path:'/api/terms/leave',body:{pageID:'test-page'}},{path:'/api/terms/return',body:{pageID:'test-page'}}]);
  assert.equal(s.windowEvents.visibilitychange,undefined);
  await s.node('setupRestoreHelp').onclick();await s.node('setupLeave').onclick();
  assert.equal(s.calls.filter(c=>c.path==='/api/hosts/disable').length,1);assert.ok(s.calls.some(c=>c.path==='/api/exit'));
  assert.equal(s.window.location.hash,'#/setup');assert.equal(s.window.swdBeforeNavigate('setup','home'),false);
});

test('first-entry failed acceptance stays locked and authenticated state failure remains retryable',async()=>{
  let saveFailed=true,stateFailed=true;
  const s=setup({firstEntry:true,step:'welcome',receipt:{accepted:false},request:async path=>({error:(path==='/api/terms/accept'&&saveFailed)||(path==='/api/status'&&stateFailed)?'暂不可用':undefined})});await flush();
  await s.node('setupAcceptTerms').onclick();assert.ok(s.window.swdTermsGate);assert.equal(s.node('setupStepStorage').disabled,true);
  saveFailed=false;await s.node('setupAcceptTerms').onclick();
  assert.equal(s.window.swdTermsGate,null);assert.equal(s.node('setupAcceptTerms').disabled,true);assert.equal(s.node('setupStepStorage').disabled,true);
  assert.match(s.node('setupMessage').textContent,/条款已确认/);
  stateFailed=false;await s.context.requestRefresh();assert.equal(s.node('setupStepStorage').disabled,false);
  const before=s.calls.length;s.windowEvents.pagehide();assert.equal(s.calls.length,before);
});

test('another window accepting unlocks first entry without reload or a second diagnosis',async()=>{
  const receipt={accepted:false};const s=setup({firstEntry:true,receipt});await flush();
  receipt.accepted=true;await s.go('welcome');await flush();
  assert.equal(s.node('setupAcceptTerms').disabled,true);assert.equal(s.node('setupStepStorage').disabled,false);
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'||c.path==='/api/setup/network'),false);
});

test('first-entry progress write failure cannot revoke consent or require navigation',async()=>{
  const s=setup({firstEntry:true,step:'welcome',receipt:{accepted:false},request:async path=>path==='/api/setup'?{error:'进度保存失败'}:{}});await flush();
  await s.node('setupAcceptTerms').onclick();await flush();
  assert.equal(s.node('setupTermsLabel').textContent,'阅读并同意');assert.equal(s.node('setupStepStorage').disabled,false);
  assert.equal(s.window.location.href,undefined);assert.equal(s.calls.filter(c=>c.path==='/api/terms/accept').length,1);
});

test('reordered free navigation only validates and stages settings without starting diagnostics',async()=>{
  const s=setup();await flush();
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/network').length,0);
  await s.go('proxy');s.node('upstreamMode').value='auto';s.node('socks5Address').value='127.0.0.1:1080';
  await s.go('storage');s.node('maxCacheGiB').value='25';
  s.node('setupImportSource').value='D:\\videos';
  await s.node('setupNext').onclick();assert.equal(s.node('setupTitle').textContent,'网络');
  await s.go('finish');
  assert.match(s.node('setupSummary').textContent,/25 GiB/);assert.match(s.node('setupSummary').textContent,/D:\\videos/);
  assert.match(s.node('setupSummary').textContent,/直连 \+ 代理/);assert.equal(business(s).length,0);
  assert.deepEqual(s.calls.filter(c=>c.path==='/api/setup/validate').map(c=>c.body.page),['proxy','storage','proxy']);
});

test('terms have a one-time confirmation button; navigation never accepts',async()=>{
  const s=setup({receipt:{accepted:false}});await flush();await s.go('welcome');await flush();
  assert.equal(s.node('setupTermsText').textContent,'完整条款');
  assert.equal(s.node('setupTermsLabel').textContent,'阅读并同意');assert.equal(s.node('setupAcceptTerms').disabled,false);
  assert.equal(s.node('setupNext').disabled,true);
  await s.node('setupNext').onclick();assert.equal(s.node('setupTitle').textContent,'开始');
  for(const id of ['setupBack','setupStepWelcome']){
    await s.node(id).onclick();assert.equal(s.node('setupTitle').textContent,'开始');
    await s.go('welcome');await flush();
  }
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'||c.path==='/api/setup/validate'),false);
});

test('all later steps require a saved valid receipt, not progress',async()=>{
  const s=setup({step:'finish',receipt:{accepted:false}});await flush();
  assert.equal(s.node('setupTitle').textContent,'开始');
  await s.go('welcome');await flush();
  for(const name of ['storage','proxy','finish']){
    assert.equal(s.node('setupStep'+name[0].toUpperCase()+name.slice(1)).disabled,true);
    await s.go(name);assert.equal(s.node('setupTitle').textContent,'开始');
  }
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'||c.path==='/api/setup/logs'||c.path==='/api/setup/storage'),false);
  assert.equal(business(s).length,0);
});

test('reading the receipt never blocks the start page or restores an obsolete step',async()=>{
  const d=deferred();let reads=0;
  const s=setup({step:'finish',request:async path=>path==='/api/terms/current'&&++reads===1?d.promise:{accepted:false}});await flush();
  assert.equal(s.node('setupTitle').textContent,'开始');assert.equal(s.node('setupStepWelcome').disabled,false);
  await s.go('welcome');await flush();await s.go('welcome');
  d.resolve({accepted:true});await flush();
  assert.equal(s.node('setupTitle').textContent,'开始');assert.equal(s.node('setupStepFinish').disabled,true);
  assert.equal(s.calls.some(c=>c.path==='/api/setup/logs'),false);
});

test('consent saves once and stays on the start page with a green check',async()=>{
  const d=deferred();const s=setup({step:'welcome',receipt:{accepted:false},request:path=>path==='/api/terms/accept'?d.promise:Promise.resolve({})});await flush();
  const pending=s.node('setupAcceptTerms').onclick();await flush();await s.node('setupAcceptTerms').onclick();
  assert.equal(s.node('setupTitle').textContent,'开始');assert.equal(s.node('setupAcceptTerms').disabled,true);
  assert.equal(s.node('setupStepFinish').disabled,true);
  assert.deepEqual(s.calls.find(c=>c.path==='/api/terms/accept').body,{version:'v1',sha256:'hash1',agree:true,contentRights:true});
  d.resolve({});await pending;
  assert.equal(s.node('setupTitle').textContent,'开始');assert.equal(s.node('setupTermsLabel').textContent,'阅读并同意');
  assert.equal(s.node('setupTermsIcon').dataset.state,'ok');
  assert.equal(s.node('setupAcceptTerms').disabled,true);assert.equal(s.node('setupNext').disabled,false);
  await s.go('finish');assert.equal(s.node('setupTitle').textContent,'启用');
  await s.go('welcome');await flush();await s.node('setupAcceptTerms').onclick();
  assert.equal(s.calls.filter(c=>c.path==='/api/terms/accept').length,1);
  assert.equal(s.calls.some(c=>c.path==='/api/setup/network'),false);assert.equal(business(s).length,0);
});

test('persisted consent is displayed after reload or reentry and cannot be toggled off',async()=>{
  const receipt={accepted:false};const s=setup({step:'welcome',receipt});await flush();
  await s.node('setupAcceptTerms').onclick();assert.equal(receipt.accepted,true);
  s.node('setupLeave').onclick();s.node('page-setup').hidden=false;s.events.pagechange();
  await s.go('welcome');await flush();assert.equal(s.node('setupAcceptTerms').disabled,true);
  const reopened=setup({step:'welcome',receipt});await flush();
  assert.equal(reopened.node('setupTermsLabel').textContent,'阅读并同意');
  await reopened.go('storage');assert.equal(reopened.calls.some(c=>c.path==='/api/terms/accept'),false);
});

test('returning to terms reloads validity and closes the gate for an invalid receipt',async()=>{
  const receipt={accepted:true};const s=setup({step:'welcome',receipt});await flush();await s.go('storage');
  receipt.accepted=false;await s.go('welcome');await flush();
  assert.equal(s.node('setupAcceptTerms').disabled,false);assert.equal(s.node('setupNext').disabled,true);
  await s.go('storage');assert.equal(s.node('setupTitle').textContent,'开始');
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'),false);
});

test('failed receipt reads allow returning to welcome and explicit read retry without accepting',async()=>{
  let failed=true;const s=setup({step:'welcome',request:async path=>path==='/api/terms/current'&&failed?{error:'读取失败'}:{}});await flush();
  assert.equal(s.node('setupNext').disabled,true);
  await s.go('welcome');await s.go('welcome');await flush();
  assert.equal(s.node('setupAcceptTerms').textContent,'重新读取条款');
  failed=false;await s.node('setupAcceptTerms').onclick();await flush();
  assert.equal(s.node('setupTermsLabel').textContent,'阅读并同意');await s.go('storage');
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'),false);
});

test('stale receipt read cannot unlock a new wizard visit',async()=>{
  const d=deferred();let reads=0;const s=setup({step:'welcome',request:async path=>path==='/api/terms/current'?(++reads===1?d.promise:{accepted:false}):{}});await flush();
  s.node('setupLeave').onclick();s.node('page-setup').hidden=false;s.events.pagechange();await s.go('welcome');await flush();
  d.resolve({accepted:true});await flush();assert.equal(s.node('setupStepStorage').disabled,true);
});

test('receipt save failure keeps the gate closed and allows retry, welcome or offline discard',async()=>{
  let failed=true;
  const s=setup({step:'welcome',receipt:{accepted:false},request:async path=>path==='/api/terms/accept'&&failed?{error:'无法安全保存同意记录'}:{}});await flush();
  await s.node('setupAcceptTerms').onclick();assert.match(s.node('setupMessage').textContent,/无法安全保存/);
  assert.equal(s.node('setupStepStorage').disabled,true);await s.go('welcome');await s.go('welcome');await flush();
  failed=false;await s.node('setupAcceptTerms').onclick();assert.equal(s.node('setupStepStorage').disabled,false);
  s.context.connected=false;s.context.renderControls();assert.equal(s.node('setupLeave').disabled,false);
  s.node('setupLeave').onclick();assert.equal(s.window.location.hash,'#/home');
});

test('late receipt response cannot navigate back into a discarded wizard',async()=>{
  const d=deferred();const s=setup({step:'welcome',receipt:{accepted:false},request:path=>path==='/api/terms/accept'?d.promise:Promise.resolve({})});await flush();
  const pending=s.node('setupAcceptTerms').onclick();await flush();s.node('setupLeave').onclick();
  s.node('page-setup').hidden=true;d.resolve({});await pending;
  assert.equal(s.node('preferences').parentNode,s.node('page-settings'));assert.equal(s.window.location.hash,'#/home');
});

test('next follows the requested sequence after one immediate confirmation',async()=>{
  const s=setup({receipt:{accepted:false}});await flush();
  assert.equal(s.node('setupTitle').textContent,'开始');
  for(const title of ['存储','网络','启用']){
    await flush();
    if(s.node('setupTitle').textContent==='开始')await s.node('setupAcceptTerms').onclick();
    await s.node('setupNext').onclick();
    assert.equal(s.node('setupTitle').textContent,title);
  }
  assert.equal(business(s).length,0);assert.equal(s.calls.filter(c=>c.path==='/api/terms/accept').length,1);
});

for(const entry of ['settings','help'])test(entry+' reentry keeps diagnostics manual with valid saved consent',async()=>{
  const s=setup(entry==='help'?{search:'?setup=1'}:{});await flush();
  if(entry==='settings'){
    s.node('setupLeave').onclick();s.node('page-setup').hidden=false;
    s.window.swdBeforeNavigate('settings','setup');s.events.pagechange();await flush();
  }else assert.equal(s.calls.find(c=>c.path==='replaceURL').url,'/#/setup');
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/network').length,0);
  await s.go('welcome');await flush();await s.go('proxy');await s.go('welcome');await flush();s.events.consolestate();
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/network').length,0);
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'),false);
  s.node('setupLeave').onclick();s.node('page-setup').hidden=false;
  s.window.swdBeforeNavigate('settings','setup');s.events.pagechange();await flush();
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/network').length,0);
});

test('entry without valid consent never requests diagnosis and confirmation does not duplicate the server trigger',async()=>{
  const s=setup({search:'?setup=1',receipt:{accepted:false}});await flush();
  assert.equal(s.calls.some(c=>c.path==='/api/setup/network'),false);
  await s.go('welcome');await flush();await s.node('setupAcceptTerms').onclick();
  await s.go('welcome');await s.go('welcome');await flush();
  assert.equal(s.calls.some(c=>c.path==='/api/setup/network'),false);
});

test('local access checks logs on entry and retry without blocking draft navigation',async()=>{
  const d=deferred();let checks=0;const s=setup({step:'finish',request:async path=>path==='/api/setup/logs'?(++checks===1?d.promise:{state:'ok',message:'已检测到可读取的 VRC 日志。',path:'D:\\logs\\output_log_test.txt'}):{}});await flush();
  vm.runInNewContext(controls,s.context);s.context.renderControls();assert.equal(s.node('setupCheckAccess').disabled,true);
  await s.go('storage');await s.go('finish');await flush();assert.match(s.node('setupLogsStatus').textContent,/已检测到/);
  d.resolve({state:'unknown',message:'旧响应'});await flush();assert.doesNotMatch(s.node('setupLogsStatus').textContent,/旧响应/);
  await s.node('setupCheckAccess').onclick();assert.equal(checks,3);assert.equal(business(s).length,0);
});

test('log inspection errors remain visible and allow continuing',async()=>{
  const s=setup({step:'finish',request:async path=>{if(path==='/api/setup/logs')throw Error('断连');return {};}});await flush();
  assert.match(s.node('setupLogsStatus').textContent,/无法确认.*断连/);await s.go('finish');assert.equal(s.node('setupTitle').textContent,'启用');
});

test('unified access refresh reads fresh hosts and ports before logs and coalesces clicks',async()=>{
  let logs=0;
  const s=setup({step:'finish',stateOverride:{portOK:true,httpsPortOK:true},request:async path=>path==='/api/setup/logs'?(logs++,{state:'ok',directory:'D:\\logs'}):{}});await flush();
  const d=deferred();let refreshes=0;
  s.context.requestRefresh=async()=>{refreshes++;await d.promise;s.context.lastState.hosts.ready=true;s.context.lastState.portOK=false;s.events.consolestate();};
  const pending=s.node('setupCheckAccess').onclick();await flush();await s.node('setupCheckAccess').onclick();
  assert.equal(refreshes,1);assert.equal(logs,1);assert.equal(s.node('setupCheckAccess').disabled,true);
  d.resolve();await pending;
  assert.equal(logs,2);assert.equal(s.node('setupHostsIcon').dataset.state,'ok');assert.equal(s.node('setupPort80Icon').dataset.state,'failed');assert.equal(s.node('setupLogsPath').textContent,'D:\\logs');assert.equal(business(s).length,0);
});

test('each departure checks validity and failed validation retains page and draft',async()=>{
  const s=setup({step:'storage',request:async(path)=>path==='/api/setup/validate'?{error:'目录不可写'}:{}});await flush();
  s.node('maxCacheGiB').value='25';await s.go('proxy');
  assert.equal(s.node('setupTitle').textContent,'存储');assert.equal(s.node('maxCacheGiB').value,'25');assert.match(s.node('setupMessage').textContent,/目录不可写/);assert.equal(business(s).length,0);
});

for(const next of ['welcome','finish'])test('valid consent does not bypass current page validation when entering '+next,async()=>{
  let invalid=true;
  const s=setup({step:'storage',request:async path=>path==='/api/setup/validate'&&invalid?{error:'目录不可写'}:{}});await flush();
  await s.go(next);assert.equal(s.node('setupTitle').textContent,'存储');assert.match(s.node('setupMessage').textContent,/目录不可写/);
  invalid=false;await s.go(next);assert.notEqual(s.node('setupTitle').textContent,'存储');
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'),false);assert.equal(business(s).length,0);
});

test('invalid native field is reported before any network transition',async()=>{
  const s=setup({step:'proxy'});await flush();s.node('socks5Address').disabled=false;s.node('socks5Address').required=true;
  await s.go('storage');assert.equal(s.node('socks5Address').reported,true);assert.equal(s.calls.some(c=>c.path==='/api/setup/validate'),false);
});

for(const id of ['setupBack','setupNext','setupStepProxy'])test(id+' saves draft only',async()=>{
  const s=setup({step:'storage'});await flush();s.node('maxCacheGiB').value='12';await s.node(id).onclick();
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/validate').length,1);assert.equal(business(s).length,0);
});

test('discard works offline with invalid fields and reentry loads original settings',async()=>{
  const s=setup({step:'storage'});await flush();s.node('maxCacheGiB').value='invalid';s.node('setupImportSource').value='D:\\source';s.context.connected=false;
  s.node('setupLeave').onclick();assert.equal(s.window.location.hash,'#/home');assert.equal(s.node('maxCacheGiB').value,'0');assert.equal(s.node('setupImportSource').value,'');
  s.node('page-setup').hidden=false;s.events.pagechange();assert.equal(s.node('setupTitle').textContent,'开始');assert.equal(business(s).length,0);
});

test('late validation cannot remount controls or resurrect discarded draft',async()=>{
  const d=deferred();const s=setup({step:'storage',request:path=>path==='/api/setup/validate'?d.promise:Promise.resolve({})});await flush();
  s.node('maxCacheGiB').value='25';const pending=s.go('proxy');await flush();
  s.node('setupLeave').onclick();s.node('page-setup').hidden=true;d.resolve({});await pending;
  assert.equal(s.node('preferences').parentNode,s.node('page-settings'));assert.equal(s.node('maxCacheGiB').value,'0');assert.equal(s.window.location.hash,'#/home');
});

test('ordinary settings draft survives a separate wizard visit and discard',async()=>{
  const s=setup();await flush();s.node('setupLeave').onclick();s.node('page-setup').hidden=true;
  s.context.settingsDirty=true;s.node('socks5Password').value='ordinary-secret';s.node('storageDir').value='ordinary draft';
  s.node('page-setup').hidden=false;s.events.pagechange();assert.notEqual(s.node('storageDir').value,'ordinary draft');
  s.node('setupLeave').onclick();assert.equal(s.node('storageDir').value,'ordinary draft');assert.equal(s.node('socks5Password').value,'ordinary-secret');
});

for(const action of ['leave','reset'])test(`wizard ${action} releases its node draft and preserves the earlier settings draft`,async()=>{
  let id=0;
  const s=setup({step:'proxy',sharedImport:true,request:async path=>path==='/api/proxy/import'?{id:'draft-'+(++id),accepted:1,items:[]}: {}});
  await flush();s.node('setupLeave').onclick();s.node('page-setup').hidden=true;
  const importNodes=async text=>{
    s.node('upstreamMode').value='proxy';s.node('proxyBackend').value='mihomo';
    s.node('proxyImportText').value=text;s.emit('settings','input');s.emit('proxyImport','click');await flush();
  };
  await importNodes('ordinary');assert.equal(s.node('proxyImportID').value,'draft-1');
  s.node('page-setup').hidden=false;s.events.pagechange();
  assert.equal(s.node('proxyImportID').value,'');
  assert.equal(s.calls.some(c=>c.path==='/api/proxy/import/discard'),false);
  await flush();await s.go('proxy');
  await importNodes('wizard');assert.equal(s.node('proxyImportID').value,'draft-2');
  if(action==='reset'){
    s.node('setupReset').onclick();assert.equal(s.node('proxyImportID').value,'');
  }
  s.node('setupLeave').onclick();await flush();
  assert.equal(s.node('proxyImportID').value,'draft-1');
  const released=s.calls.filter(c=>c.path==='/api/proxy/import/discard');
  assert.equal(released.length,1);assert.equal(released[0].body.id,'draft-2');
  s.node('page-setup').hidden=true;await s.context.saveSettings();
  assert.equal(s.calls.find(c=>c.path==='/api/settings').body.proxyImportID,'draft-1');
});

test('reset affects current page only and never starts import',async()=>{
  const s=setup({step:'storage'});await flush();s.node('socks5Address').value='127.0.0.1:1080';s.node('upstreamMode').value='auto';s.node('maxCacheGiB').value='25';
  s.context.renderControls();s.node('setupReset').onclick();assert.equal(s.node('maxCacheGiB').value,'0');assert.equal(s.node('socks5Address').value,'127.0.0.1:1080');assert.equal(s.node('setupReset').disabled,true);
  await s.go('proxy');s.node('maxCacheGiB').value='12';s.context.renderControls();s.node('setupReset').onclick();assert.equal(s.node('upstreamMode').value,'direct');assert.equal(s.node('maxCacheGiB').value,'12');
  await s.go('storage');s.node('setupImportSource').value='D:\\videos';s.emit('setupImportSource','input');s.node('setupReset').onclick();assert.equal(s.node('setupImportSource').value,'');assert.equal(business(s).length,0);
});

test('only final confirmation applies full plan; import does not start on page visits',async()=>{
  const s=setup({request:async(path,body)=>path==='/api/setup/apply'?{id:body.id,phase:'completed',configSaved:true,importStarted:true}:{}});await flush();
  await s.go('storage');s.node('setupImportSource').value='D:\\videos';await s.go('finish');
  assert.equal(business(s).length,0);await s.node('setupNext').onclick();
  const requests=business(s);assert.equal(requests.length,1);assert.equal(requests[0].path,'/api/setup/apply');assert.equal(requests[0].body.importSource,'D:\\videos');assert.equal(requests[0].body.page,undefined);
  assert.equal(s.window.location.hash,'#/home');assert.equal(s.calls.some(c=>c.path==='/api/start'||c.path.startsWith('/api/hosts/')),false);
});

test('lost acknowledgement only queries the original receipt and never resubmits the plan',async()=>{
  let ids=[];const s=setup({step:'finish',request:async(path,body)=>{if(path==='/api/setup/apply'){ids.push(body.id);throw Error('connection lost');}return {};}});await flush();
  await s.node('setupNext').onclick();await s.node('setupNext').onclick();assert.equal(ids.length,1);assert.equal(s.memory.get('swd-setup-application'),ids[0]);
  const queries=s.calls.filter(c=>c.path==='/api/setup/receipt');assert.equal(queries.length,2);assert.ok(queries.every(c=>c.body.id===ids[0]));
});

test('reload looks up its own receipt when another window replaced the shared receipt',async()=>{
  const s=setup({applicationID:'original',search:'?setup=1',stateOverride:{setupApplication:{id:'other',phase:'completed'}},request:async path=>path==='/api/setup/receipt'?{id:'original',phase:'completed',configSaved:true,importStarted:true}:{}});await flush();
  assert.equal(s.memory.has('swd-setup-application'),false);assert.equal(s.window.location.hash,'#/home');
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/receipt').length,1);
  assert.equal(s.calls.some(c=>['/api/setup/apply','/api/setup/network','/api/restart','/api/import/start'].includes(c.path)),false);
});

test('receipt read failure keeps its ID across retry and explicit exit',async()=>{
  let failed=true;
  const s=setup({applicationID:'original',request:async path=>path==='/api/setup/receipt'?(failed?{error:'暂时不可读'}:{id:'original',phase:'completed'}):{}});await flush();
  assert.match(s.node('setupApplyResult').textContent,/暂时不可读/);assert.equal(s.node('setupNext').textContent,'核对应用结果');
  s.node('setupLeave').onclick();assert.equal(s.memory.get('swd-setup-application'),'original');
  failed=false;s.node('page-setup').hidden=false;s.events.pagechange();await flush();
  assert.equal(s.window.location.hash,'#/home');assert.equal(s.memory.has('swd-setup-application'),false);assert.equal(business(s).length,0);
});

test('only a confirmed missing receipt can be forgotten on exit, without automatic replay',async()=>{
  const s=setup({applicationID:'original',request:async path=>path==='/api/setup/receipt'?{status:404,error:'未找到原回执'}:{}});await flush();
  assert.match(s.node('setupApplyResult').textContent,/未找到/);assert.equal(s.memory.get('swd-setup-application'),'original');
  assert.equal(business(s).length,0);s.node('setupLeave').onclick();assert.equal(s.memory.has('swd-setup-application'),false);
});

test('late receipt lookup cannot resurrect a discarded wizard',async()=>{
  const d=deferred();const s=setup({applicationID:'original',request:path=>path==='/api/setup/receipt'?d.promise:Promise.resolve({})});await flush();
  s.node('setupLeave').onclick();s.node('page-setup').hidden=true;d.resolve({id:'original',phase:'restart'});await flush();
  assert.equal(s.calls.some(c=>c.path==='reconnect'),false);assert.equal(s.window.location.hash,'#/home');
});

test('restart response reconnects without a second restart command',async()=>{
  const s=setup({step:'finish',request:async(path,body)=>path==='/api/setup/apply'?{id:body.id,phase:'restart',configSaved:true}:{}});await flush();
  await s.node('setupNext').onclick();assert.equal(s.calls.filter(c=>c.path==='reconnect').length,1);assert.equal(s.calls.some(c=>c.path==='/api/restart'),false);
});

test('restart timeout exposes wizard recovery, preserves the ID and never resubmits the plan',async()=>{
  let attempts=0;
  const s=setup({step:'finish',reconnect:async()=>++attempts===1?{state:'timeout',message:'60 秒内未连接，请重新连接并核对。'}:{state:'reloading'},request:async(path,body)=>path==='/api/setup/apply'?{id:body.id,phase:'restart',configSaved:true}:{}});await flush();
  await s.node('setupNext').onclick();
  s.context.connected=false;s.context.renderControls();s.events.consolestate();
  assert.match(s.node('setupApplyResult').textContent,/配置已保存.*60 秒.*重新连接/);
  assert.equal(s.node('setupNext').disabled,false);assert.equal(s.node('setupNext').textContent,'重新连接并核对');
  assert.equal(s.node('setupLeave').disabled,false);assert.equal(attempts,1);
  assert.equal(s.memory.get('swd-setup-application'),'application-1');
  await s.node('setupNext').onclick();assert.equal(attempts,2);
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/apply').length,1);
  assert.equal(s.calls.some(c=>c.path==='/api/restart'||c.path==='/api/import/start'),false);
});

test('timed-out application can leave and reload using its original receipt',async()=>{
  const response={id:'existing-application',phase:'restart',configSaved:true};
  const s=setup({step:'finish',applicationID:response.id,stateOverride:{setupApplication:response},reconnect:async()=>({state:'timeout',message:'未连接，请手动启动应用。'})});await flush();
  assert.equal(s.node('setupLeave').disabled,false);
  assert.equal(s.window.swdBeforeNavigate('setup','home'),true);
  assert.equal(s.memory.get('swd-setup-application'),response.id);
  const resumed=setup({step:'finish',applicationID:s.memory.get('swd-setup-application'),search:'?setup=1',stateOverride:{setupApplication:{...response,phase:'completed'}}});await flush();
  assert.equal(resumed.window.location.hash,'#/home');assert.equal(resumed.memory.has('swd-setup-application'),false);
  assert.equal(resumed.calls.some(c=>c.path==='/api/setup/apply'||c.path==='/api/setup/network'),false);
});

test('browser route exit prompts for staged changes; cancellation keeps every field',async()=>{
  let allow=false;
  const s=setup({step:'storage',confirm:()=>allow});await flush();
  s.node('maxCacheGiB').value='25';await s.go('proxy');
  s.node('upstreamMode').value='auto';s.node('socks5Address').value='127.0.0.1:1080';
  assert.equal(s.window.swdBeforeNavigate('setup','settings'),false);
  assert.equal(s.node('maxCacheGiB').value,'25');assert.equal(s.node('socks5Address').value,'127.0.0.1:1080');
  assert.equal(s.node('setupTitle').textContent,'网络');assert.equal(business(s).length,0);
  allow=true;assert.equal(s.window.swdBeforeNavigate('setup','settings'),true);
  assert.equal(s.node('maxCacheGiB').value,'0');assert.equal(s.calls.filter(c=>c.path==='confirm').length,2);
});

test('unchanged or restored drafts leave without prompting; explicit discard asks no second time',async()=>{
  const s=setup({step:'storage',confirm:()=>{throw Error('unexpected prompt');}});await flush();
  s.node('maxCacheGiB').value='25';s.context.renderControls();s.node('setupReset').onclick();
  assert.equal(s.window.swdBeforeNavigate('setup','settings'),true);
  s.node('page-setup').hidden=false;s.events.pagechange();await flush();await s.go('storage');
  s.node('maxCacheGiB').value='25';s.node('setupLeave').onclick();
  assert.equal(s.window.location.hash,'#/home');assert.equal(s.node('maxCacheGiB').value,'0');
});

test('closing or reloading uses native beforeunload for any unapplied draft',async()=>{
  const s=setup({step:'storage'});await flush();
  const fire=()=>{let prevented=false;const event={preventDefault(){prevented=true;}};s.windowEvents.beforeunload(event);return {event,prevented};};
  assert.equal(fire().prevented,false);
  s.node('maxCacheGiB').value='25';await s.go('welcome');await flush();
  assert.equal(fire().prevented,true);assert.equal(fire().event.returnValue,'');
  s.node('maxCacheGiB').value='0';assert.equal(fire().prevented,false);
  s.node('setupImportSource').value='D:\\source';assert.equal(fire().prevented,true);
});

test('hosts button applies immediately without saving draft and does not block navigation',async()=>{
  const d=deferred();const s=setup({step:'finish',request:path=>path==='/api/hosts/enable'?d.promise:Promise.resolve({})});await flush();
  const pending=s.node('setupHostsEnable').onclick();await flush();
  await s.node('setupHostsEnable').onclick();assert.equal(s.node('setupHostsEnable').disabled,true);
  assert.equal(s.calls.filter(c=>c.path==='/api/hosts/enable').length,1);assert.equal(s.node('setupStepStorage').disabled,false);
  assert.equal(s.node('setupNext').disabled,false);
  await s.go('storage');assert.equal(s.node('setupTitle').textContent,'存储');assert.equal(business(s).length,0);
  d.resolve({});await pending;
});

test('hosts buttons use confirmed state, allow failure retry and restore explicitly',async()=>{
  let failed=true;
  const s=setup({step:'finish',request:async path=>{
    if(path==='/api/hosts/enable'){
      if(failed)return {error:'UAC 已取消'};
      s.context.lastState.hosts.ready=true;
    }
    if(path==='/api/hosts/disable')s.context.lastState.hosts.ready=false;
    return {};
  }});await flush();
  assert.equal(s.node('setupStepFinish').dataset.status,'default');
  await s.node('setupHostsEnable').onclick();
  assert.equal(s.node('setupHostsEnable').disabled,false);assert.match(s.node('setupHostsResult').textContent,/UAC 已取消/);
  assert.equal(s.node('setupStepFinish').dataset.status,'default');
  failed=false;await s.node('setupHostsEnable').onclick();
  assert.equal(s.node('setupHostsResult').textContent,'');
  assert.equal(s.node('setupHostsEnable').disabled,true);assert.equal(s.node('setupHostsEnable').hidden,true);assert.equal(s.node('setupHostsIcon').dataset.state,'ok');
  assert.equal(s.node('setupHostsRestore').hidden,false);assert.equal(s.node('setupStepFinish').dataset.status,'changed');
  await s.node('setupHostsEnable').onclick();assert.equal(s.calls.filter(c=>c.path==='/api/hosts/enable').length,2);
  await s.node('setupHostsRestore').onclick();assert.equal(s.node('setupHostsEnable').disabled,false);
  assert.equal(s.node('setupHostsResult').textContent,'');
  assert.equal(s.node('setupStepFinish').dataset.status,'default');assert.equal(business(s).length,0);
});

test('step markers follow visits and current defaults, reset and discard without granting access',async()=>{
  const s=setup({receipt:{accepted:false}});await flush();
  assert.equal(s.node('setupStepWelcome').dataset.status,'default');
  assert.equal(s.node('setupStepStorage').dataset.status,'unvisited');
  await s.go('welcome');await flush();assert.equal(s.node('setupStepWelcome').dataset.status,'default');
  await s.node('setupAcceptTerms').onclick();assert.equal(s.node('setupStepWelcome').dataset.status,'changed');
  await s.go('storage');assert.equal(s.node('setupStepStorage').dataset.status,'default');
  s.node('maxCacheGiB').value='25';s.emit('settings','input');
  assert.equal(s.node('setupStepStorage').dataset.status,'changed');
  await s.go('storage');s.node('setupImportSource').value='D:\\source';s.emit('setupImportSource','input');
  assert.equal(s.node('setupStepStorage').dataset.status,'changed');
  await s.go('storage');s.node('setupReset').onclick();
  assert.equal(s.node('setupStepStorage').dataset.status,'default');assert.equal(s.node('setupImportSource').value,'');
  s.node('setupLeave').onclick();s.node('page-setup').hidden=false;s.events.pagechange();await flush();
  assert.equal(s.node('setupStepStorage').dataset.status,'unvisited');
  assert.equal(s.node('setupStepWelcome').dataset.status,'changed');assert.equal(business(s).length,0);
});

test('saved non-default values are marked before visiting but never unlock consent gates',async()=>{
  const s=setup({receipt:{accepted:false},stateOverride:{settings:{storageDir:'D:\\cache',maxCacheBytes:0,upstreamMode:'direct'}}});await flush();
  assert.equal(s.node('setupStepStorage').dataset.status,'changed');assert.equal(s.node('setupStepStorage').disabled,true);
  await s.go('storage');assert.equal(s.node('setupTitle').textContent,'开始');
});

test('global refresh keeps draft editable offline and never overwrites it',async()=>{
  const s=setup({step:'storage'});await flush();s.node('maxCacheGiB').value='20';s.context.connected=false;
  vm.runInNewContext(controls,s.context);s.context.renderControls();s.context.renderSettings(s.context.lastState);
  assert.equal(s.node('maxCacheGiB').disabled,false);assert.equal(s.node('maxCacheGiB').value,'20');assert.equal(s.node('setupLeave').disabled,false);
});

test('terms modal stays readable and closable under global API gating without accepting',async()=>{
  const s=setup({firstEntry:true,receipt:{accepted:false}});await flush();
  vm.runInNewContext(controls,s.context);s.context.renderControls();
  assert.equal(s.node('setupShowTerms').disabled,false);await s.node('setupShowTerms').onclick();
  assert.equal(s.node('setupTermsDialog').open,true);assert.equal(s.node('setupTermsText').textContent,'完整条款');
  s.context.renderControls();assert.equal(s.node('setupCloseTerms').disabled,false);
  s.node('setupCloseTerms').onclick();assert.equal(s.node('setupTermsDialog').open,false);
  assert.equal(s.node('setupNext').disabled,true);assert.equal(s.node('setupTermsIcon').dataset.state,'unknown');
  assert.equal(s.calls.filter(c=>c.path==='/api/terms/accept').length,0);
});

test('an unverifiable receipt opens the reusable modal; failed save keeps it open and gated',async()=>{
  let fail=true;
  const s=setup({firstEntry:true,receipt:{accepted:false},request:async path=>path==='/api/terms/current'?{needsConfirmation:true}:path==='/api/terms/accept'&&fail?{error:'写入失败'}:{}});await flush();
  assert.equal(s.node('setupTermsDialog').open,true);assert.match(s.node('setupTermsNotice').textContent,/无法验证/);
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'),false);
  await s.node('setupAcceptTerms').onclick();assert.equal(s.node('setupTermsDialog').open,true);assert.match(s.node('setupTermsResult').textContent,/写入失败/);assert.equal(s.node('setupNext').disabled,true);
  fail=false;await s.node('setupAcceptTerms').onclick();assert.equal(s.node('setupTermsDialog').open,false);assert.equal(s.node('setupTermsIcon').dataset.state,'ok');assert.equal(s.node('setupTermsLabel').textContent,'阅读并同意');
  await s.node('setupShowTerms').onclick();assert.equal(s.node('setupTermsDialog').open,true);assert.equal(s.node('setupAcceptTerms').disabled,true);assert.equal(s.node('setupTermsNotice').hidden,true);
});

test('fresh first entry does not auto-open terms or restore hosts on hover',async()=>{
  const s=setup({firstEntry:true,receipt:{accepted:false}});await flush();
  assert.ok(!s.node('setupTermsDialog').open);
  s.emit('setupRestoreHelp','mouseenter');assert.equal(s.calls.some(c=>c.path==='/api/hosts/disable'),false);
  const pending=deferred();let restores=0;
  const r=setup({firstEntry:true,receipt:{accepted:false},request:async path=>path==='/api/hosts/disable'?(restores++,pending.promise):{}});await flush();
  const click=r.node('setupRestoreHelp').onclick();await flush();assert.equal(r.node('setupRestoreHelp').disabled,true);
  await r.node('setupRestoreHelp').onclick();assert.equal(restores,1);pending.resolve({});await click;assert.equal(r.node('setupRestoreHelp').disabled,false);
});

test('custom storage and upstream checkbox constraints remain enforced',async()=>{
  const s=setup({step:'storage'});await flush();s.node('setupCustomStorage').checked=true;s.emit('setupCustomStorage','change');s.node('storageDir').value='C:\\Portable\\other';s.context.renderControls();assert.match(s.node('storageDir').validationMessage,/程序文件夹/);
  s.node('storageDir').value='D:\\media';s.node('setupDefaultStorage').onclick();assert.equal(s.node('storageDir').value,s.context.lastState.defaultStorageDir);
  for(const [a,b,mode]of [[true,false,'direct'],[false,true,'proxy'],[true,true,'auto'],[false,false,'']]){s.node('setupDirect').checked=a;s.node('setupSocks5').checked=b;s.emit('setupDirect','input');assert.equal(s.node('upstreamMode').value,mode);}
});

test('discard clears wizard-only invalidity before returning to ordinary settings',async()=>{
  const s=setup({step:'proxy'});await flush();
  s.node('setupDirect').checked=false;s.node('setupSocks5').checked=false;s.emit('setupDirect','input');s.context.renderControls();
  assert.ok(s.node('setupDirect').validationMessage);
  s.node('setupLeave').onclick();
  assert.equal(s.node('setupDirect').validationMessage,'');assert.equal(s.node('upstreamMode').validationMessage,'');assert.equal(s.node('setupDirect').disabled,true);
});

test('pending validation freezes edited fields while discard remains available',async()=>{
  const d=deferred();const s=setup({step:'storage',request:path=>path==='/api/setup/validate'?d.promise:Promise.resolve({})});await flush();
  const pending=s.go('finish');assert.equal(s.node('maxCacheGiB').disabled,true);assert.equal(s.node('setupLeave').disabled,false);
  d.resolve({});await pending;
});

test('console polling and navigation never start diagnostics or overwrite event-owned results',async()=>{
 const s=setup({step:'proxy',search:'?setup=1'});await flush();
 for(const id of ['setupCatalogStatus','setupPlaybackStatus','setupResourceStatus'])s.node(id).textContent='event-owned';
 s.context.lastState.setupNetwork={running:true,monitor:{results:[{operation:'resource',state:'available',estimatedSpeedBPS:900000}]}};s.events.consolestate();
 for(const id of ['setupCatalogStatus','setupPlaybackStatus','setupResourceStatus'])assert.equal(s.node(id).textContent,'event-owned');
 assert.equal(s.calls.some(c=>c.path.startsWith('/api/setup/network')),false);
});
