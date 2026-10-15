// Manual diagnostics share one upstream selector. Both panels render only SSE
// events; CSS animates pending ellipses without a refresh timer.
(() => {
  const node = id => document.getElementById(id);
  const get = id => node('setupCalibration' + id);
  if (!get('')) return;
  const upstream = node('setupNetworkUpstream');
  const panels = {
    connectivity:{prefix:'setupConnectivity',label:'连通性检测'},
    calibration:{prefix:'setupCalibration',label:'CF 并发测速'},
  };
  for (const p of Object.values(panels)) Object.assign(p,{stream:null,online:false,pending:false,state:null,revision:-1,discard:false,intent:null});
  const visible = () => !document.hidden && !window.swdTermsGate && !node('page-setup').hidden && !get('').closest('[data-setup-step]').hidden;
  const text = (id,value) => { const el=node(id); if(el.textContent!==value) el.textContent=value; };
  const saved = p => p.state?.lastValid?.[upstream.value];
  const valid = value => typeof value === 'number' && Number.isFinite(value) && value >= 0;
  const number = value => value.toFixed(2);
  function controls() {
    const all = Object.values(panels);
    upstream.disabled = all.some(p => !p.online || p.pending || p.intent || p.state?.running || p.state?.closed);
    for (const p of all) {
      const running = !!p.state?.running || p.intent?.action === 'start';
      const button = node(p.prefix+'Toggle');
      button.dataset.running = String(running);
      text(p.prefix+'Tooltip',running ? '放弃检测' : '请先准备好日常游戏的网络环境，保持常用加速器、代理和 TUN 设置，并暂停其他下载，再手动开始');
      button.setAttribute('aria-label', running ? '停止并放弃'+p.label : '开始'+p.label);
      button.setAttribute('aria-busy', String(!!p.pending || running));
      button.disabled = !p.online || !!p.pending || !!p.intent || !!p.state?.closed || p.state?.phase === 'canceling' || all.some(other => other !== p && (other.pending || other.intent || other.state?.running));
    }
  }
  function metric(id,value,unit) { text('setupCalibration'+id,(valid(value)?number(value):'—')+unit); }
  // Equal-width decision zones, logarithmic inside each zone. The explicit
  // ticks show the change in scale while retaining space for 100x+ links.
  function gaugePosition(ratio) {
    const value=Math.min(256,Math.max(.25,ratio));
    if(value<=1)return Math.log2(value/.25)/2/3*100;
    if(value<=2)return (1+Math.log2(value))/3*100;
    return (2+Math.log2(value/2)/7)/3*100;
  }
  function renderCalibration() {
    const p=panels.calibration,s=p.state || {},previous=saved(p);
    const discarded=p.discard || ['canceling','canceled','failed'].includes(s.phase);
    const matches=!s.run || upstream.value===s.upstream;
    const complete=s.phase==='completed' && valid(s.singleSpeedBPS) && valid(s.fullSpeedBPS);
    const restore=discarded || !matches || (!s.running && !complete);
    const result=restore ? previous || (matches && !discarded ? s : {}) : s;
    const live=result===s && !!s.running && !discarded;
    const stages={single_warmup:'单路预热…',single:'单路测速…',ramping:'逐渐增加并发…',full_warmup:'满路预热…',full:'满路测速…',complete:'正在结束…'};
    const phases={idle:'尚未测速',preparing:'准备视频…',running:stages[s.measurementStage] || '测速中…',completed:complete?'测速完成':'样本不足',failed:'测速未完成',canceling:'正在停止…',canceled:previous?'已恢复上次结果':'已放弃检测'};
    let status=!matches ? previous?'上次有效结果':'尚未测速' : p.intent?.action==='start' ? '正在启动…' : discarded && s.running?'正在停止…':phases[s.phase] || '尚未测速';
    if(s.phase==='failed' && previous)status='未完成，保留上次结果';
    if(matches && s.phase==='preparing' && s.preparationProgress>0)status='准备视频… '+s.preparationProgress+'%';
    if(matches && s.phase==='failed' && s.reason==='preparation_insufficient')status='视频准备不足，请重试'+(previous?'（保留上次结果）':'');
    text('setupCalibrationStatus',status);
    const singleStage=['single_warmup','single'].includes(s.measurementStage);
    const single=live ? s.liveSingleSpeedBPS ?? s.singleSpeedBPS ?? (singleStage?s.liveSpeedBPS:null) : result.singleSpeedBPS;
    const full=live ? s.liveFullSpeedBPS ?? s.fullSpeedBPS ?? (!singleStage?s.liveSpeedBPS:null) : result.fullSpeedBPS;
    metric('SingleSpeed',valid(single)?single*8/1e6:null,' Mbps');
    metric('FullSpeed',valid(full)?full*8/1e6:null,' Mbps');
    const concurrency=live?s.open:result.fullConcurrency;
    text('setupCalibrationLimit',concurrency>0?String(concurrency):'—');
    metric('CPUAverage',result.fullCPUPercent,'%');metric('CPUP99',result.cpuP99Percent,'%');
    const gaugeSpeed=live?s.liveSpeedBPS ?? (singleStage?single:full):full;
    const ratio=valid(gaugeSpeed) && result.referenceBitrateBPS>0 ? gaugeSpeed*8/result.referenceBitrateBPS : null;
    const usable=valid(ratio) && (result!==s || ['running','completed'].includes(s.phase));
    get('Pointer').hidden=!usable;get('PointerValue').hidden=!usable;
    const basis='以 2 Mbps 为 1×；分段对数刻度 0.25×—1×—2×—256×';
    if(!usable) { get('Gauge').setAttribute('aria-label','尚无速度样本；'+basis+'；≤1 倍不可用，1 至 2 倍不稳定，>2 倍可用');return; }
    const advice=ratio>2?'可用':ratio>1?'不稳定':'不可用',position=gaugePosition(ratio);
    get('Pointer').style.left=position+'%';
    get('PointerValue').style.left=position+'%';
    text('setupCalibrationPointerValue',number(ratio)+'×');
    get('Gauge').setAttribute('aria-label',(live?'当前 ':'满路 ')+number(ratio)+' 倍，'+advice+(live?'（测量中）':'')+'；'+basis+(ratio>256?'；超过刻度上限':ratio<.25?'；低于刻度下限':''));
  }
  function renderConnectivity() {
    const p=panels.connectivity,s=p.state || {};
    const discarded=p.discard || ['canceling','canceled'].includes(s.phase);
    const matches=!s.run || s.upstream === upstream.value;
    const rows=matches && !discarded ? s.results || [] : saved(p) || [];
    for(const [operation,id] of [['catalog','Catalog'],['playback_url','Playback'],['resource','Resource']]) {
      const r=rows.find(row=>row.operation===operation);
      const checking=r?.state==='checking';
      const result=node('setup'+id+'Status'),icon=node('setup'+id+'Icon');
      let label=checking?'检测中':r?.state==='available'?'可连接':r?.state==='failed'?'连接失败':r?.state==='skipped'?'未检测（前项未通过）':s.running && !discarded && matches?'等待检测':'尚未检测';
      if(valid(r?.latencyMS)) label+=' · 响应 '+Math.round(r.latencyMS)+' ms';
      if(result.textContent!==label)result.textContent=label;result.classList.toggle('setup-detecting',checking);
      const status=r?.state==='available'?'ok':r?.state==='failed'?'failed':'unknown';
      icon.textContent=status==='ok'?'✓':status==='failed'?'!':'○';icon.dataset.state=status;
    }
    text('setupConnectivityStatus',p.intent?.action==='start'?'正在启动…':discarded?s.running?'正在停止…':saved(p)?'已恢复上次结果':'已放弃检测':!matches?saved(p)?'上次有效结果':'尚未检测':'');
  }
  function render() { controls(); renderConnectivity(); renderCalibration(); }
  function disconnect() {
    for(const p of Object.values(panels)) { p.stream?.close();p.stream=null;p.online=false; }
    controls();
  }
  function connect() {
    if(!visible()) { disconnect();return; }
    for(const [kind,p] of Object.entries(panels)) {
      if(p.stream) continue;
      p.revision=-1;
      if(typeof EventSource==='undefined') { text(p.prefix+'Connection','此浏览器不支持检测事件连接，请使用新版浏览器。');continue; }
      text(p.prefix+'Connection','正在连接检测状态…');
      const stream=new EventSource('/api/setup/network/'+kind+'/events');p.stream=stream;
      stream.addEventListener(kind,event=>{
        if(p.stream!==stream) return;
        try {
          const next=JSON.parse(event.data);
          if(typeof next.revision!=='number') throw Error('invalid event');
          p.online=true;text(p.prefix+'Connection','');
          if(next.revision!==p.revision) {
            if(next.running && next.upstream) upstream.value=next.upstream;
            if(next.run!==p.state?.run) p.discard=false;
            p.state=next;p.revision=next.revision;
            if(p.intent && (next.run!==p.intent.run || p.intent.action==='cancel' && (!next.running || next.phase==='canceling')))p.intent=null;
          }
          render();
          if(next.closed) { stream.close();p.stream=null;p.online=false;controls();text(p.prefix+'Connection','应用正在关闭。'); }
        } catch(_) { p.online=false;controls();text(p.prefix+'Connection','检测状态无法读取，请重新进入网络页。'); }
      });
      stream.onerror=()=>{if(p.stream!==stream)return;p.online=false;controls();text(p.prefix+'Connection','检测状态连接已断开，正在重新连接…');};
    }
    controls();
  }
  async function action(kind) {
    const p=panels[kind];
    if(node(p.prefix+'Toggle').disabled) return;
    const action=p.state?.running?'cancel':'start';
    p.pending=action;p.intent={action,run:p.state?.run || 0};if(action==='cancel')p.discard=true;render();text(p.prefix+'Connection','');
    try {
      const body=action==='start'?{upstream:upstream.value}:{};
      if(body.upstream==='proxy') {
        if(!window.swdSetupCalibrationPlan)throw Error('代理草稿尚未就绪，请重新进入引导页');
        body.proxy=window.swdSetupCalibrationPlan();
      }
      const response=await fetch('/api/setup/network/'+kind+'/'+action,{method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':token},body:JSON.stringify(body),signal:AbortSignal.timeout(15000)});
      if(!response.ok) { const message=await response.text();let detail=message;try{detail=JSON.parse(message).error || message;}catch(_){}throw Error(detail); }
    } catch(error) {
      p.intent=null;
      // A failed POST does not confirm cancellation. Resume rendering the SSE
      // state; confirmed canceling/canceled phases still restore saved results.
      if(action==='cancel')p.discard=false;
      text(p.prefix+'Connection','操作未确认：'+error.message+'。请根据推送状态核对。');
    }
    finally {p.pending=false;render();}
  }
  for(const kind of Object.keys(panels)) node(panels[kind].prefix+'Toggle').onclick=()=>action(kind);
  upstream.onchange=render;
  document.addEventListener('setupstep',connect);
  document.addEventListener('pagechange',connect);
  document.addEventListener('visibilitychange',connect);
  window.addEventListener('pagehide',disconnect);
  window.addEventListener('pageshow',connect);
  connect();
})();
