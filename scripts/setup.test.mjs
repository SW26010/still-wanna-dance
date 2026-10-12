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
function deferred(){let resolve;const promise=new Promise(r=>resolve=r);return {resolve,promise};}
function setup({step='welcome',request=async()=>({}),stateOverride={},receipt={accepted:true},search='',reconnect=async()=>({state:'reloading'}),confirm=()=>true,applicationID=''}={}) {
  const nodes=new Map(),events={},windowEvents={},calls=[];
  const node=id=>{
    if(!nodes.has(id))nodes.set(id,{id,value:'',checked:false,hidden:false,dataset:{},listeners:{},
      addEventListener(name,fn){(this.listeners[name] ||= []).push(fn);},
      dispatchEvent(e){for(const f of this.listeners[e.type]||[])f(e);},
      setAttribute(k,v){this[k]=v;},removeAttribute(k){delete this[k];},getAttribute(k){return this[k]||null;},
      setCustomValidity(v){this.validationMessage=v;},checkValidity(){return !this.validationMessage && !(this.required&&!this.value);},
      reportValidity(){this.reported=true;},focus(){this.focused=true;},
      closest(selector){return selector==='[data-setup-settings]'?this.group:null;},
      querySelectorAll(){return [];},before(marker){marker.parentNode=this.parentNode;},after(child){child.parentNode=this.parentNode;},
      append(...children){for(const child of children)child.parentNode=this;},
    });return nodes.get(id);
  };
  const groups=['storage','proxy'].map(name=>{const n=node(name+'Group');n.dataset.setupSettings=name;return n;});
  const ids=['storageDir','maxCacheGiB','upstreamMode','socks5Address','socks5Username','socks5Password','setupCustomStorage','setupDirect','setupSocks5'];
  for(const id of ids)node(id).group=groups[['storageDir','maxCacheGiB','setupCustomStorage'].includes(id)?0:1];
  node('settings').querySelectorAll=()=>ids.map(node);
  node('preferences').parentNode=node('page-settings');
  const window={location:{hash:'#/setup',search,pathname:'/',assign(url){this.href=url;}},history:{replaceState(_,__,url){calls.push({path:'replaceURL',url});}},confirm:message=>{calls.push({path:'confirm',message});return confirm();},swdInitialRoute:'#/setup',addEventListener(name,fn){windowEvents[name]=fn;}};
  const memory=new Map(applicationID?[['swd-setup-application',applicationID]]:[]);
  const state={settings:{storageDir:'C:\\Portable\\data\\storage',maxCacheBytes:0,upstreamMode:'direct',socks5Address:'',socks5Username:''},defaultStorageDir:'C:\\Portable\\data\\storage',portableDir:'C:\\Portable',hosts:{ready:false},queue:{},batch:{},setupSettingsVersion:'v1',...stateOverride};
  const context={window,globalThis:window,document:{
    createComment:node,querySelectorAll:s=>s==='button'?buttons.map(node):s==='[data-setup-settings]'?groups:[],
    addEventListener(n,f){events[n]=f;},dispatchEvent(){}},
    $:node,settingsDirty:false,settingsBaseline:'original',lastState:state,connected:true,busy:false,exiting:false,lastInventory:null,
    token:'fixture',AbortSignal,Event,URLSearchParams,CustomEvent:class{},crypto:{randomUUID:()=> 'application-1'},
    sessionStorage:{getItem:k=>memory.get(k),setItem:(k,v)=>memory.set(k,v),removeItem:k=>memory.delete(k)},
    activationPending:()=>false,healthImpact:()=>'',renderStableList(){},
    renderSettings(s){if(window.swdSetupOwnsSettings?.())return;for(const [id,key]of [['storageDir','storageDir'],['upstreamMode','upstreamMode'],['socks5Address','socks5Address'],['socks5Username','socks5Username']])node(id).value=s.settings[key]||'';node('maxCacheGiB').value=String((s.settings.maxCacheBytes||0)/1073741824);node('socks5Password').value='';window.swdSetupSettingsLoaded?.(s);context.settingsBaseline='wizard';},
    renderControls(){window.swdSetupRenderControls?.();},setText:(id,value)=>{node(id).textContent=value;},
    requestRefresh:async()=>{events.consolestate?.();},reconnectAfterRestart:async()=>{calls.push({path:'reconnect'});return reconnect();},
    fetch:async(path,options={})=>{
      const body=options.body?JSON.parse(options.body):null;calls.push({path,body});
      if(path==='/api/setup'&&!options.method)return {ok:true,json:async()=>({step,state:'active'})};
      if(path==='/api/setup/storage')return {ok:true,json:async()=>({space:{state:'ok'},writable:{state:'ok',message:'可写'},library:{state:'ok',message:'新库'},freeBytes:1,totalBytes:2})};
      const data=await request(path,body);
      if(path==='/api/terms/accept'&&!data.error)receipt.accepted=true;
      return {ok:!data.error,json:async()=>path==='/api/terms/current'?{text:'完整条款',version:'v1',sha256:'hash1',accepted:receipt.accepted,...data}:data,text:async()=>data.error||'完整条款'};
    },
  };
  vm.runInNewContext(script,context);
  return {node,window,context,events,windowEvents,calls,memory,
    go:name=>node('setupStep'+name[0].toUpperCase()+name.slice(1)).onclick(),
    emit:(id,event)=>node(id).dispatchEvent(new Event(event)),
  };
}
const business=s=>s.calls.filter(c=>['/api/settings','/api/setup/apply','/api/import/start','/api/activation/enable'].includes(c.path));

test('reordered free navigation only validates and stages settings without starting diagnostics',async()=>{
  const s=setup();await flush();
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/network').length,0);
  await s.go('proxy');s.node('upstreamMode').value='auto';s.node('socks5Address').value='127.0.0.1:1080';
  await s.go('storage');s.node('maxCacheGiB').value='25';
  await s.node('setupNext').onclick();assert.equal(s.node('setupTitle').textContent,'导入视频');
  s.node('setupImportSource').value='D:\\videos';await s.go('finish');
  assert.match(s.node('setupSummary').textContent,/25 GiB/);assert.match(s.node('setupSummary').textContent,/D:\\videos/);
  assert.match(s.node('setupSummary').textContent,/直连 \+ SOCKS5/);assert.equal(business(s).length,0);
  assert.deepEqual(s.calls.filter(c=>c.path==='/api/setup/validate').map(c=>c.body.page),['proxy','storage','import']);
});

test('terms have a one-time confirmation button; navigation never accepts',async()=>{
  const s=setup({receipt:{accepted:false}});await flush();await s.go('terms');await flush();
  assert.equal(s.node('setupTermsText').textContent,'完整条款');
  assert.equal(s.node('setupAcceptTerms').textContent,'确认同意条款');assert.equal(s.node('setupAcceptTerms').disabled,false);
  assert.equal(s.node('setupNext').disabled,true);
  await s.node('setupNext').onclick();assert.equal(s.node('setupTitle').textContent,'使用条款');
  for(const id of ['setupBack','setupStepWelcome']){
    await s.node(id).onclick();assert.equal(s.node('setupTitle').textContent,'欢迎使用');
    await s.go('terms');await flush();
  }
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'||c.path==='/api/setup/validate'),false);
});

test('all later steps require a saved valid receipt, not progress',async()=>{
  const s=setup({step:'finish',receipt:{accepted:false}});await flush();
  assert.equal(s.node('setupTitle').textContent,'欢迎使用');
  await s.go('terms');await flush();
  for(const name of ['storage','import','direct','proxy','environment','finish']){
    assert.equal(s.node('setupStep'+name[0].toUpperCase()+name.slice(1)).disabled,true);
    await s.go(name);assert.equal(s.node('setupTitle').textContent,'使用条款');
  }
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'||c.path==='/api/setup/logs'||c.path==='/api/setup/storage'),false);
  assert.equal(business(s).length,0);
});

test('reading the receipt never blocks the first two steps or restores an obsolete step',async()=>{
  const d=deferred();let reads=0;
  const s=setup({step:'environment',request:async path=>path==='/api/terms/current'&&++reads===1?d.promise:{accepted:false}});await flush();
  assert.equal(s.node('setupTitle').textContent,'欢迎使用');assert.equal(s.node('setupStepTerms').disabled,false);
  await s.go('terms');await flush();await s.go('welcome');
  d.resolve({accepted:true});await flush();
  assert.equal(s.node('setupTitle').textContent,'欢迎使用');assert.equal(s.node('setupStepEnvironment').disabled,true);
  assert.equal(s.calls.some(c=>c.path==='/api/setup/logs'),false);
});

test('confirmation saves immediately, stays on terms and only then unlocks later steps',async()=>{
  const d=deferred();const s=setup({step:'terms',receipt:{accepted:false},request:path=>path==='/api/terms/accept'?d.promise:Promise.resolve({})});await flush();
  const pending=s.node('setupAcceptTerms').onclick();await flush();await s.node('setupAcceptTerms').onclick();
  assert.equal(s.node('setupTitle').textContent,'使用条款');assert.equal(s.node('setupAcceptTerms').disabled,true);
  assert.equal(s.node('setupStepFinish').disabled,true);
  assert.deepEqual(s.calls.find(c=>c.path==='/api/terms/accept').body,{version:'v1',sha256:'hash1',agree:true,contentRights:true});
  d.resolve({});await pending;
  assert.equal(s.node('setupTitle').textContent,'使用条款');assert.equal(s.node('setupAcceptTerms').textContent,'已确认条款');
  assert.equal(s.node('setupAcceptTerms').disabled,true);assert.equal(s.node('setupNext').disabled,false);
  await s.go('finish');assert.equal(s.node('setupTitle').textContent,'核对应用');
  await s.go('terms');await flush();await s.node('setupAcceptTerms').onclick();
  assert.equal(s.calls.filter(c=>c.path==='/api/terms/accept').length,1);
  assert.equal(s.calls.some(c=>c.path==='/api/setup/network'),false);assert.equal(business(s).length,0);
});

test('persisted consent is displayed after reload or reentry and cannot be toggled off',async()=>{
  const receipt={accepted:false};const s=setup({step:'terms',receipt});await flush();
  await s.node('setupAcceptTerms').onclick();assert.equal(receipt.accepted,true);
  s.node('setupLeave').onclick();s.node('page-setup').hidden=false;s.events.pagechange();
  await s.go('terms');await flush();assert.equal(s.node('setupAcceptTerms').disabled,true);
  const reopened=setup({step:'terms',receipt});await flush();
  assert.equal(reopened.node('setupAcceptTerms').textContent,'已确认条款');
  await reopened.go('storage');assert.equal(reopened.calls.some(c=>c.path==='/api/terms/accept'),false);
});

test('returning to terms reloads validity and closes the gate for an invalid receipt',async()=>{
  const receipt={accepted:true};const s=setup({step:'terms',receipt});await flush();await s.go('storage');
  receipt.accepted=false;await s.go('terms');await flush();
  assert.equal(s.node('setupAcceptTerms').disabled,false);assert.equal(s.node('setupNext').disabled,true);
  await s.go('storage');assert.equal(s.node('setupTitle').textContent,'使用条款');
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'),false);
});

test('failed receipt reads allow returning to welcome and explicit read retry without accepting',async()=>{
  let failed=true;const s=setup({step:'terms',request:async path=>path==='/api/terms/current'&&failed?{error:'读取失败'}:{}});await flush();
  assert.equal(s.node('setupNext').disabled,true);
  await s.go('welcome');await s.go('terms');await flush();
  assert.equal(s.node('setupAcceptTerms').textContent,'重新读取条款');
  failed=false;await s.node('setupAcceptTerms').onclick();await flush();
  assert.equal(s.node('setupAcceptTerms').textContent,'已确认条款');await s.go('storage');
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'),false);
});

test('stale receipt read cannot unlock a new wizard visit',async()=>{
  const d=deferred();let reads=0;const s=setup({step:'terms',request:async path=>path==='/api/terms/current'?(++reads===1?d.promise:{accepted:false}):{}});await flush();
  s.node('setupLeave').onclick();s.node('page-setup').hidden=false;s.events.pagechange();await s.go('terms');await flush();
  d.resolve({accepted:true});await flush();assert.equal(s.node('setupStepStorage').disabled,true);
});

test('receipt save failure keeps the gate closed and allows retry, welcome or offline discard',async()=>{
  let failed=true;
  const s=setup({step:'terms',receipt:{accepted:false},request:async path=>path==='/api/terms/accept'&&failed?{error:'无法安全保存同意记录'}:{}});await flush();
  await s.node('setupAcceptTerms').onclick();assert.match(s.node('setupMessage').textContent,/无法安全保存/);
  assert.equal(s.node('setupStepStorage').disabled,true);await s.go('welcome');await s.go('terms');await flush();
  failed=false;await s.node('setupAcceptTerms').onclick();assert.equal(s.node('setupStepStorage').disabled,false);
  s.context.connected=false;s.context.renderControls();assert.equal(s.node('setupLeave').disabled,false);
  s.node('setupLeave').onclick();assert.equal(s.window.location.hash,'#/home');
});

test('late receipt response cannot navigate back into a discarded wizard',async()=>{
  const d=deferred();const s=setup({step:'terms',receipt:{accepted:false},request:path=>path==='/api/terms/accept'?d.promise:Promise.resolve({})});await flush();
  const pending=s.node('setupAcceptTerms').onclick();await flush();s.node('setupLeave').onclick();
  s.node('page-setup').hidden=true;d.resolve({});await pending;
  assert.equal(s.node('preferences').parentNode,s.node('page-settings'));assert.equal(s.window.location.hash,'#/home');
});

test('next follows the requested sequence after one immediate confirmation',async()=>{
  const s=setup({receipt:{accepted:false}});await flush();
  for(const title of ['使用条款','存储设置','导入视频','网络检测','上游设置','本机接入','核对应用']){
    await flush();
    if(s.node('setupTitle').textContent==='使用条款')await s.node('setupAcceptTerms').onclick();
    await s.node('setupNext').onclick();assert.equal(s.node('setupTitle').textContent,title);
  }
  assert.equal(business(s).length,0);assert.equal(s.calls.filter(c=>c.path==='/api/terms/accept').length,1);
});

for(const entry of ['settings','help'])test(entry+' reentry starts one direct diagnosis with valid saved consent',async()=>{
  const s=setup(entry==='help'?{search:'?setup=1'}:{});await flush();
  if(entry==='settings'){
    s.node('setupLeave').onclick();s.node('page-setup').hidden=false;
    s.window.swdBeforeNavigate('settings','setup');s.events.pagechange();await flush();
  }else assert.equal(s.calls.find(c=>c.path==='replaceURL').url,'/#/setup');
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/network').length,1);
  await s.go('terms');await flush();await s.go('direct');await s.go('terms');await flush();s.events.consolestate();
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/network').length,1);
  assert.equal(s.calls.some(c=>c.path==='/api/terms/accept'),false);
  s.node('setupLeave').onclick();s.node('page-setup').hidden=false;
  s.window.swdBeforeNavigate('settings','setup');s.events.pagechange();await flush();
  assert.equal(s.calls.filter(c=>c.path==='/api/setup/network').length,2);
});

test('entry without valid consent never requests diagnosis and confirmation does not duplicate the server trigger',async()=>{
  const s=setup({search:'?setup=1',receipt:{accepted:false}});await flush();
  assert.equal(s.calls.some(c=>c.path==='/api/setup/network'),false);
  await s.go('terms');await flush();await s.node('setupAcceptTerms').onclick();
  await s.go('welcome');await s.go('terms');await flush();
  assert.equal(s.calls.some(c=>c.path==='/api/setup/network'),false);
});

test('local access checks logs on entry and retry without blocking draft navigation',async()=>{
  const d=deferred();let checks=0;const s=setup({step:'environment',request:async path=>path==='/api/setup/logs'?(++checks===1?d.promise:{state:'ok',message:'已检测到可读取的 VRC 日志。',path:'D:\\logs\\output_log_test.txt'}):{}});await flush();
  vm.runInNewContext(controls,s.context);s.context.renderControls();assert.equal(s.node('setupCheckLogs').disabled,true);
  await s.go('storage');await s.go('environment');await flush();assert.match(s.node('setupLogsStatus').textContent,/已检测到/);
  d.resolve({state:'unknown',message:'旧响应'});await flush();assert.doesNotMatch(s.node('setupLogsStatus').textContent,/旧响应/);
  await s.node('setupCheckLogs').onclick();assert.equal(checks,3);assert.equal(business(s).length,0);
});

test('log inspection errors remain visible and allow continuing',async()=>{
  const s=setup({step:'environment',request:async path=>{if(path==='/api/setup/logs')throw Error('断连');return {};}});await flush();
  assert.match(s.node('setupLogsStatus').textContent,/无法确认.*断连/);await s.go('finish');assert.equal(s.node('setupTitle').textContent,'核对应用');
});

test('each departure checks validity and failed validation retains page and draft',async()=>{
  const s=setup({step:'storage',request:async(path)=>path==='/api/setup/validate'?{error:'目录不可写'}:{}});await flush();
  s.node('maxCacheGiB').value='25';await s.go('proxy');
  assert.equal(s.node('setupTitle').textContent,'存储设置');assert.equal(s.node('maxCacheGiB').value,'25');assert.match(s.node('setupMessage').textContent,/目录不可写/);assert.equal(business(s).length,0);
});

for(const next of ['welcome','terms','finish'])test('valid consent does not bypass current page validation when entering '+next,async()=>{
  let invalid=true;
  const s=setup({step:'storage',request:async path=>path==='/api/setup/validate'&&invalid?{error:'目录不可写'}:{}});await flush();
  await s.go(next);assert.equal(s.node('setupTitle').textContent,'存储设置');assert.match(s.node('setupMessage').textContent,/目录不可写/);
  invalid=false;await s.go(next);assert.notEqual(s.node('setupTitle').textContent,'存储设置');
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
  s.node('page-setup').hidden=false;s.events.pagechange();assert.equal(s.node('setupTitle').textContent,'欢迎使用');assert.equal(business(s).length,0);
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

test('reset affects current page only and never starts import',async()=>{
  const s=setup({step:'storage'});await flush();s.node('socks5Address').value='127.0.0.1:1080';s.node('upstreamMode').value='auto';s.node('maxCacheGiB').value='25';
  s.context.renderControls();s.node('setupReset').onclick();assert.equal(s.node('maxCacheGiB').value,'0');assert.equal(s.node('socks5Address').value,'127.0.0.1:1080');assert.equal(s.node('setupReset').disabled,true);
  await s.go('proxy');s.node('maxCacheGiB').value='12';s.context.renderControls();s.node('setupReset').onclick();assert.equal(s.node('upstreamMode').value,'direct');assert.equal(s.node('maxCacheGiB').value,'12');
  await s.go('import');s.node('setupImportSource').value='D:\\videos';s.emit('setupImportSource','input');s.node('setupReset').onclick();assert.equal(s.node('setupImportSource').value,'');assert.equal(business(s).length,0);
});

test('only final confirmation applies full plan; import does not start on page visits',async()=>{
  const s=setup({request:async(path,body)=>path==='/api/setup/apply'?{id:body.id,phase:'completed',configSaved:true,importStarted:true}:{}});await flush();
  await s.go('import');s.node('setupImportSource').value='D:\\videos';await s.go('finish');
  assert.equal(business(s).length,0);await s.node('setupNext').onclick();
  const requests=business(s);assert.equal(requests.length,1);assert.equal(requests[0].path,'/api/setup/apply');assert.equal(requests[0].body.importSource,'D:\\videos');assert.equal(requests[0].body.page,undefined);
  assert.equal(s.node('setupNext').textContent,'返回控制台');assert.equal(s.node('setupLeave').textContent,'返回控制台');
});

test('application retries use the same id after a lost acknowledgement',async()=>{
  let ids=[];const s=setup({step:'finish',request:async(path,body)=>{if(path==='/api/setup/apply'){ids.push(body.id);throw Error('connection lost');}return {};}});await flush();
  await s.node('setupNext').onclick();await s.node('setupNext').onclick();assert.equal(ids.length,2);assert.equal(ids[0],ids[1]);assert.equal(s.memory.get('swd-setup-application'),ids[0]);
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
  assert.match(resumed.node('setupApplyResult').textContent,/清单已应用/);
  assert.equal(resumed.calls.some(c=>c.path==='/api/setup/apply'||c.path==='/api/setup/network'),false);
});

test('browser route exit prompts for staged changes; cancellation keeps every field',async()=>{
  let allow=false;
  const s=setup({step:'storage',confirm:()=>allow});await flush();
  s.node('maxCacheGiB').value='25';await s.go('proxy');
  s.node('upstreamMode').value='auto';s.node('socks5Address').value='127.0.0.1:1080';
  assert.equal(s.window.swdBeforeNavigate('setup','settings'),false);
  assert.equal(s.node('maxCacheGiB').value,'25');assert.equal(s.node('socks5Address').value,'127.0.0.1:1080');
  assert.equal(s.node('setupTitle').textContent,'上游设置');assert.equal(business(s).length,0);
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
  s.node('maxCacheGiB').value='25';await s.go('terms');await flush();
  assert.equal(fire().prevented,true);assert.equal(fire().event.returnValue,'');
  s.node('maxCacheGiB').value='0';assert.equal(fire().prevented,false);
  s.node('setupImportSource').value='D:\\source';assert.equal(fire().prevented,true);
});

test('hosts switch applies immediately without saving draft and does not block navigation',async()=>{
  const d=deferred();const s=setup({step:'environment',request:path=>path==='/api/hosts/enable'?d.promise:Promise.resolve({})});await flush();
  s.node('setupHostsEnabled').checked=true;s.emit('setupHostsEnabled','change');await flush();
  assert.equal(s.calls.filter(c=>c.path==='/api/hosts/enable').length,1);assert.equal(s.node('setupStepStorage').disabled,false);
  await s.go('storage');assert.equal(s.node('setupTitle').textContent,'存储设置');assert.equal(business(s).length,0);
  d.resolve({});await flush();
});

test('global refresh keeps draft editable offline and never overwrites it',async()=>{
  const s=setup({step:'storage'});await flush();s.node('maxCacheGiB').value='20';s.context.connected=false;
  vm.runInNewContext(controls,s.context);s.context.renderControls();s.context.renderSettings(s.context.lastState);
  assert.equal(s.node('maxCacheGiB').disabled,false);assert.equal(s.node('maxCacheGiB').value,'20');assert.equal(s.node('setupLeave').disabled,false);
});

test('custom storage and upstream checkbox constraints remain enforced',async()=>{
  const s=setup({step:'storage'});await flush();s.node('setupCustomStorage').checked=true;s.emit('setupCustomStorage','change');s.node('storageDir').value='C:\\Portable\\other';s.context.renderControls();assert.match(s.node('storageDir').validationMessage,/Portable/);
  s.node('storageDir').value='D:\\media';s.node('setupCustomStorage').checked=false;s.emit('setupCustomStorage','change');assert.equal(s.node('storageDir').value,s.context.lastState.defaultStorageDir);
  for(const [a,b,mode]of [[true,false,'direct'],[false,true,'socks5'],[true,true,'auto'],[false,false,'']]){s.node('setupDirect').checked=a;s.node('setupSocks5').checked=b;s.emit('setupDirect','input');assert.equal(s.node('upstreamMode').value,mode);}
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
