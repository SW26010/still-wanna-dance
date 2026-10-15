import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';
const script=readFileSync(new URL('../internal/console/assets/setup-calibration.js',import.meta.url),'utf8');
const hint='请先准备好日常游戏的网络环境，保持常用加速器、代理和 TUN 设置，并暂停其他下载，再手动开始';
function panel() {
  const nodes=new Map(),events={},windowEvents={},streams=[],requests=[];
  const section={hidden:false};
  const node=id=>{if(!nodes.has(id)){const classes=new Set();nodes.set(id,{textContent:'',value:'',disabled:false,hidden:false,style:{},dataset:{},attributes:{},classList:{toggle(name,on){on?classes.add(name):classes.delete(name);},contains:name=>classes.has(name)},setAttribute(name,value){this.attributes[name]=value;},closest(){return section;}});}return nodes.get(id);};
  const get=id=>node('setupCalibration'+id),upstream=node('setupNetworkUpstream');upstream.value='direct';
  const document={hidden:false,getElementById:node,addEventListener(name,fn){events[name]=fn;}};
  class EventSource {
    constructor(url){this.url=url;this.events={};streams.push(this);}
    addEventListener(name,fn){this.events[name]=fn;}
    close(){this.closed=true;}
    send(data){this.events[this.url.split('/').at(-2)]({data:JSON.stringify(data)});}
  }
  const window={swdTermsGate:false,addEventListener(name,fn){windowEvents[name]=fn;}};
  const context={document,window,EventSource,token:'test-token',AbortSignal:{timeout:()=>undefined},fetch:async(url,options)=>{requests.push({url,options});return {ok:true};},setInterval(){throw Error('UI polling forbidden');},setTimeout(){throw Error('UI polling forbidden');}};
  vm.runInNewContext(script,context);
  const stream=kind=>streams.findLast(s=>s.url.includes('/'+kind+'/'));
  const send=(kind,state)=>stream(kind).send(state);
  const ready=()=>{send('connectivity',idle);send('calibration',idle);};
  return {node,get,upstream,section,document,window,events,windowEvents,streams,requests,stream,send,ready,context};
}
const idle={revision:0,run:0,upstream:'direct',phase:'idle',running:false,referenceBitrateBPS:2_000_000};
const running={...idle,revision:2,run:1,phase:'running',running:true,concurrencyLimit:16,open:16,fullConcurrency:16,measurementStage:'full',singleSpeedBPS:125_000,liveFullSpeedBPS:750_000};
test('two event streams never start work; buttons toggle state and hover text',async()=>{
 const p=panel();assert.equal(p.streams.length,2);assert.equal(p.requests.length,0);p.ready();
 for(const kind of ['Connectivity','Calibration'])assert.equal(p.node('setup'+kind+'Tooltip').textContent,hint);
 await p.get('Toggle').onclick();assert.match(p.requests[0].url,/calibration\/start$/);assert.deepEqual(JSON.parse(p.requests[0].options.body),{upstream:'direct'});
 p.send('calibration',running);assert.equal(p.get('Toggle').disabled,false);assert.equal(p.get('Toggle').dataset.running,'true');assert.equal(p.get('Tooltip').textContent,'放弃检测');assert.equal(p.node('setupConnectivityToggle').disabled,true);
 await p.get('Toggle').onclick();assert.match(p.requests[1].url,/calibration\/cancel$/);assert.equal(p.get('Results').hidden,false);assert.equal(p.get('Pointer').hidden,true);
 p.send('calibration',{...running,revision:3,phase:'canceled',running:false});assert.equal(p.get('Tooltip').textContent,hint);assert.equal(p.get('Toggle').dataset.running,'false');assert.equal(p.get('Results').hidden,false);
});
test('connectivity proceeds by events, pending ellipsis, latency only and cancel discards all rows',async()=>{
 const p=panel();p.ready();await p.node('setupConnectivityToggle').onclick();assert.match(p.requests[0].url,/connectivity\/start$/);
 p.send('connectivity',{...running,results:[{operation:'catalog',state:'checking'}]});assert.equal(p.node('setupCatalogStatus').classList.contains('setup-detecting'),true);assert.equal(p.node('setupPlaybackStatus').textContent,'等待检测');
 p.send('connectivity',{...running,revision:3,results:[{operation:'catalog',state:'available',latencyMS:18.5},{operation:'playback_url',state:'checking'}]});assert.equal(p.node('setupCatalogStatus').textContent,'可连接 · 响应 19 ms');assert.equal(p.node('setupCatalogStatus').classList.contains('setup-detecting'),false);assert.equal(p.node('setupPlaybackStatus').classList.contains('setup-detecting'),true);
 p.send('connectivity',{...running,revision:4,results:[{operation:'resource',state:'available',latencyMS:30,estimatedSpeedBPS:900000}]});assert.equal(p.node('setupResourceStatus').textContent,'可连接 · 响应 30 ms');
 await p.node('setupConnectivityToggle').onclick();assert.match(p.requests[1].url,/connectivity\/cancel$/);assert.equal(p.node('setupResourceStatus').textContent,'尚未检测');assert.equal(p.node('setupResourceIcon').dataset.state,'unknown');
});
test('both checks use the shared SOCKS5 draft without applying settings',async()=>{
 const p=panel();p.ready();const draft={baseVersion:'test',proxyBackend:'external-socks5',socks5Address:'127.0.0.1:7891'};p.window.swdSetupCalibrationPlan=()=>({...draft});
 p.upstream.value='proxy';p.upstream.onchange();
 await p.node('setupConnectivityToggle').onclick();p.send('connectivity',{...idle,run:1,revision:2,phase:'completed',upstream:'proxy'});await p.get('Toggle').onclick();
 for(const r of p.requests)assert.deepEqual(JSON.parse(r.options.body),{upstream:'proxy',proxy:draft});
 p.send('connectivity',{...running,upstream:'proxy'});assert.equal(p.upstream.disabled,true);assert.equal(p.get('Toggle').disabled,true);
});
test('live phase means update both values and pointer; incomplete final samples are not conclusions',()=>{
 const p=panel();p.ready();p.send('calibration',{...running,measurementStage:'single',concurrencyLimit:1,open:1,singleSpeedBPS:null,liveSingleSpeedBPS:200000,liveFullSpeedBPS:null});
 assert.equal(p.get('SingleSpeed').textContent,'1.60 Mbps');assert.equal(p.get('FullRow').hidden,false);assert.equal(p.get('Pointer').hidden,false);assert.equal(p.get('Limit').textContent,'1');
 p.send('calibration',{...running,revision:3});assert.equal(p.get('FullSpeed').textContent,'6.00 Mbps');assert.ok(Math.abs(parseFloat(p.get('Pointer').style.left)-(2+Math.log2(1.5)/7)/3*100)<1e-9);assert.equal(p.get('Limit').textContent,'16');
 p.send('calibration',{...running,revision:4,liveFullSpeedBPS:250000});assert.ok(Math.abs(parseFloat(p.get('Pointer').style.left)-100/3)<1e-9);
 p.send('calibration',{...running,revision:5,running:false,phase:'completed',reason:'samples_exhausted',fullSpeedBPS:null});assert.equal(p.get('FullSpeed').textContent,'— Mbps');assert.equal(p.get('Pointer').hidden,true);
});
test('CPU mean is full-load only, the sole percentile is overall P99, including zero',()=>{
 const p=panel();p.ready();p.send('calibration',{...running,fullCPUPercent:0.7,cpuPercent:88,cpuP95Percent:55,cpuP99Percent:1.2});
 assert.equal(p.get('CPUAverage').textContent,'0.70%');assert.equal(p.get('CPUP99').textContent,'1.20%');
 p.send('calibration',{...running,revision:3,running:false,phase:'completed',fullCPUPercent:0,cpuP99Percent:0});assert.equal(p.get('CPUAverage').textContent,'0.00%');assert.equal(p.get('CPUP99').textContent,'0.00%');
});
test('unrounded ratio boundaries and capped gauge retain correct accessibility summary',()=>{
 const p=panel();p.ready();let revision=10;
 for(const [ratio,advice] of [[0,'不可用'],[1,'不可用'],[1.000001,'不稳定'],[2,'不稳定'],[2.000001,'可用'],[195.23,'可用'],[1024,'可用']]) {
 p.send('calibration',{...running,revision:revision++,phase:'completed',running:false,fullSpeedBPS:ratio*250000});assert.match(p.get('Gauge').attributes['aria-label'],new RegExp('，'+advice));assert.equal(p.get('PointerValue').textContent,ratio.toFixed(2)+'×');assert.ok(parseFloat(p.get('Pointer').style.left)>=0 && parseFloat(p.get('Pointer').style.left)<=100);
 }
 assert.match(p.get('Gauge').attributes['aria-label'],/超过刻度上限/);
});
test('navigation, reconnect, duplicate events and upstream changes never replay tests or relabel results',()=>{
 const p=panel();p.ready();p.send('calibration',running);const old=p.stream('calibration');
 p.section.hidden=true;p.events.setupstep();assert.equal(old.closed,true);old.send({...running,revision:100,liveFullSpeedBPS:0});assert.equal(p.get('FullSpeed').textContent,'6.00 Mbps');
 p.section.hidden=false;p.events.setupstep();p.send('connectivity',idle);p.send('calibration',{...running,revision:3,phase:'completed',running:false,fullSpeedBPS:750000});
 p.upstream.value='proxy';p.upstream.onchange();assert.equal(p.get('Results').hidden,false);assert.equal(p.get('Pointer').hidden,true);
 p.send('calibration',{...running,revision:3,phase:'completed',running:false,fullSpeedBPS:1});assert.equal(p.upstream.value,'proxy');
 p.upstream.value='direct';p.upstream.onchange();assert.equal(p.get('FullSpeed').textContent,'6.00 Mbps');assert.equal(p.requests.length,0);
 p.stream('calibration').onerror();assert.equal(p.get('Toggle').disabled,true);
});
test('failed, canceled, malformed and closed events cannot claim availability',()=>{
 const p=panel();p.ready();let revision=10;
 for(const update of [{phase:'failed'},{phase:'canceled'},{referenceBitrateBPS:0},{fullSpeedBPS:-1}]) {p.send('calibration',{...running,running:false,phase:'completed',fullSpeedBPS:1000000,...update,revision:revision++});assert.equal(p.get('Pointer').hidden,true);}
 p.send('calibration',{...idle,revision:revision++,closed:true});assert.equal(p.get('Toggle').disabled,true);assert.equal(p.stream('calibration').closed,true);
});
test('pre-consent and hidden pages never subscribe, pending operations cannot be duplicated',async()=>{
 const p=panel();p.ready();p.window.swdTermsGate=true;p.events.setupstep();assert.ok(p.streams.every(s=>s.closed));p.events.pagechange();assert.equal(p.streams.length,2);
 p.window.swdTermsGate=false;p.events.setupstep();p.ready();let resolve;p.context.fetch=()=>new Promise(r=>resolve=r);
 const pending=p.get('Toggle').onclick();assert.equal(p.get('Toggle').disabled,true);assert.equal(p.upstream.disabled,true);assert.equal(p.get('Tooltip').textContent,'放弃检测');await p.get('Toggle').onclick();resolve({ok:true});await pending;
});
test('markup has one selector, two icon buttons and labels inside their colored intervals',()=>{
 const html=readFileSync(new URL('../internal/console/setup.html',import.meta.url),'utf8');
 assert.equal((html.match(/id="setupNetworkUpstream"/g)||[]).length,1);assert.equal((html.match(/class="setup-test-toggle"/g)||[]).length,2);
 assert.match(html,/setup-calibration-zones[^>]*><span>不可用<\/span><span>不稳定<\/span><span>可用/);
 assert.doesNotMatch(html,/setupCalibrationCPUP95|setupCalibrationCancel|setupRetryNetwork|暂无有效测速|setupNetworkUpstreamHint/);
 assert.match(html,/满路稳定采样阶段的平均 CPU 使用率/);assert.match(html,/正式下载全程的 CPU P99/);assert.doesNotMatch(html,/setupCalibrationAdviceDetail|setupCalibrationResults" hidden/);
});

test('cancel restores complete observations for the selected upstream, including on reconnect',async()=>{
 const p=panel();p.ready();const previous={...running,running:false,fullSpeedBPS:750000,fullCPUPercent:.7,cpuP99Percent:1.2};
 const lastValid={direct:previous,proxy:{...previous,upstream:'proxy',fullSpeedBPS:250000}};
 p.send('calibration',{...running,lastValid,run:2,revision:5,liveFullSpeedBPS:100000});
 await p.get('Toggle').onclick();assert.equal(p.get('FullSpeed').textContent,'6.00 Mbps');assert.equal(p.get('CPUAverage').textContent,'0.70%');assert.equal(p.get('PointerValue').textContent,'3.00×');
 p.send('calibration',{...running,lastValid,run:2,revision:6,running:false,phase:'canceled'});assert.match(p.get('Status').textContent,/恢复上次/);
 p.section.hidden=true;p.events.setupstep();p.section.hidden=false;p.events.setupstep();p.send('connectivity',idle);p.send('calibration',{...idle,lastValid,run:2,revision:6,phase:'canceled'});assert.equal(p.get('FullSpeed').textContent,'6.00 Mbps');
 p.upstream.value='proxy';p.upstream.onchange();assert.equal(p.get('FullSpeed').textContent,'2.00 Mbps');
 const rows=[{operation:'catalog',state:'available',latencyMS:18},{operation:'playback_url',state:'failed'},{operation:'resource',state:'skipped'}];
 p.send('connectivity',{...idle,revision:4,run:2,phase:'canceled',upstream:'proxy',lastValid:{proxy:rows}});assert.equal(p.node('setupCatalogStatus').textContent,'可连接 · 响应 18 ms');
});
test('idle, preparation and measurements preserve metric placeholders and pending start does not flash ready',async()=>{
 const p=panel();p.ready();assert.equal(p.get('SingleSpeed').textContent,'— Mbps');assert.equal(p.get('CPUP99').textContent,'—%');assert.equal(p.get('Limit').textContent,'—');
 await p.get('Toggle').onclick();assert.equal(p.get('Toggle').dataset.running,'true');assert.equal(p.get('Toggle').disabled,true);
 p.send('calibration',{...idle,run:1,revision:1,phase:'preparing',running:true});assert.equal(p.get('Toggle').dataset.running,'true');assert.equal(p.get('Toggle').disabled,false);
 for(const id of ['Results','SingleRow','FullRow','FullCPURow','CPURow'])assert.equal(p.get(id).hidden,false);
 p.send('calibration',running);for(const id of ['Results','SingleRow','FullRow','FullCPURow','CPURow'])assert.equal(p.get(id).hidden,false);
});

test('arrow follows smoothed current speed through warmup, single, ramp and full while count follows open capacity',()=>{
 const p=panel();p.ready();let revision=10;
 for(const [measurementStage,open,speed] of [['single_warmup',1,100000],['single',1,200000],['ramping',2,400000],['ramping',9,800000],['full_warmup',16,1000000],['full',16,1200000]]) {
   p.send('calibration',{...running,revision:revision++,measurementStage,open,liveSpeedBPS:speed,liveFullSpeedBPS:null,singleSpeedBPS:null});
   assert.equal(p.get('Pointer').hidden,false);assert.equal(p.get('PointerValue').textContent,(speed/250000).toFixed(2)+'×');assert.equal(p.get('Limit').textContent,String(open));
   assert.match(p.get('Gauge').attributes['aria-label'],/^当前 /);
 }
 p.send('calibration',{...running,revision:revision++,phase:'completed',running:false,open:1,fullConcurrency:12,fullSpeedBPS:750000,liveSpeedBPS:0});
 assert.equal(p.get('Limit').textContent,'12');assert.equal(p.get('PointerValue').textContent,'3.00×');
});

test('preparation progress and insufficient reserve have explicit status and preserve last valid result',()=>{
 const p=panel();p.ready();
 p.send('calibration',{...idle,revision:1,run:1,phase:'preparing',running:true,preparationProgress:42});
 assert.equal(p.get('Status').textContent,'准备视频… 42%');assert.equal(p.get('Pointer').hidden,true);
 p.send('calibration',{...idle,revision:2,run:1,phase:'failed',reason:'preparation_insufficient'});
 assert.equal(p.get('Status').textContent,'视频准备不足，请重试');assert.equal(p.get('SingleSpeed').textContent,'— Mbps');
 const previous={...running,running:false,fullSpeedBPS:750000};
 p.send('calibration',{...idle,revision:3,run:2,phase:'failed',reason:'preparation_insufficient',lastValid:{direct:previous}});
 assert.equal(p.get('Status').textContent,'视频准备不足，请重试（保留上次结果）');assert.equal(p.get('FullSpeed').textContent,'6.00 Mbps');
});

for(const kind of ['calibration','connectivity']) {
 test(kind+' resumes pushed running state when cancel POST fails, and permits retry',async()=>{
  const p=panel();p.ready();const prefix=kind==='calibration'?'setupCalibration':'setupConnectivity';
  const state={...running,results:[{operation:'catalog',state:'checking'}]};
  p.send(kind,state);
  p.context.fetch=async()=>{throw Error('request not sent');};
  await p.node(prefix+'Toggle').onclick();
  assert.match(p.node(prefix+'Connection').textContent,/操作未确认/);
  assert.doesNotMatch(p.node(prefix+'Status').textContent,/正在停止/);
  assert.equal(p.node(prefix+'Toggle').disabled,false);
  p.send(kind,{...state,revision:3,liveFullSpeedBPS:1000000});
  assert.equal(p.node(prefix+'Connection').textContent,'');
  assert.doesNotMatch(p.node(prefix+'Status').textContent,/正在停止/);
  if(kind==='calibration')assert.equal(p.get('FullSpeed').textContent,'8.00 Mbps');
  else assert.equal(p.node('setupCatalogStatus').classList.contains('setup-detecting'),true);
  let retried=false;p.context.fetch=async url=>{assert.match(url,/cancel$/);retried=true;return {ok:true};};
  await p.node(prefix+'Toggle').onclick();assert.equal(retried,true);assert.equal(p.node(prefix+'Status').textContent,'正在停止…');
 });
 for(const phase of ['canceling','canceled']) {
  test(kind+' honors pushed '+phase+' even if cancel response is lost',async()=>{
   const p=panel();p.ready();const prefix=kind==='calibration'?'setupCalibration':'setupConnectivity';
   const previous={...running,running:false,fullSpeedBPS:1000000};
   const rows=[{operation:'catalog',state:'available',latencyMS:18}];
   const state={...running,results:[{operation:'catalog',state:'checking'}],lastValid:{direct:kind==='calibration'?previous:rows}};
   p.send(kind,state);
   p.context.fetch=async()=>{p.send(kind,{...state,revision:3,phase,running:phase==='canceling'});throw Error('response lost');};
   await p.node(prefix+'Toggle').onclick();
   if(kind==='calibration')assert.equal(p.get('FullSpeed').textContent,'8.00 Mbps');
   else assert.equal(p.node('setupCatalogStatus').textContent,'可连接 · 响应 18 ms');
   assert.equal(p.node(prefix+'Status').textContent,phase==='canceling'?'正在停止…':'已恢复上次结果');
   assert.equal(p.node(prefix+'Toggle').disabled,phase==='canceling');
  });
 }
}
