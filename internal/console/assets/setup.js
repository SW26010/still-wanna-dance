// The wizard owns a per-visit draft. Navigation validates it but never writes
// business settings. Only the review page can approve an application.
(() => {
  const steps = ['welcome','terms','storage','import','direct','proxy','environment','finish'];
  const titles = ['欢迎使用','使用条款','存储设置','导入视频','网络检测','上游设置','本机接入','核对应用'];
  let step = 'welcome', loading = true, entered = false, advancing = false;
  let navigationRevision = 0, storageRequest = 0, checkedPath = '', customPath = '', terms = null;
  let termsRequest = 0;
  let logsRequest = 0, logsChecking = false;
  let draft = null, baseVersion = '', original = null, applying = false, application = null, hostsUpdating = false;
  let reconnecting = false, restartProblem = '', draftBaseline = '';
  const entryParams = new URLSearchParams(window.location.search || '');
  let diagnoseOnEntry = entryParams.get('setup') === '1';
  if (diagnoseOnEntry) {
    entryParams.delete('setup');
    window.history.replaceState(null,'',window.location.pathname + (entryParams.size ? '?' + entryParams : '') + window.location.hash);
  }
  let progressWrites = Promise.resolve();
  let applicationID = '';
  try { applicationID = sessionStorage.getItem('swd-setup-application') || ''; } catch (_) { /* optional reconnect hint */ }
  const pathKey = path => String(path || '').replaceAll('/', '\\').replace(/\\+$/, '').toLowerCase();
  const isOpen = () => !$('page-setup').hidden;
  // Step access depends on the server's saved receipt, never form validity.
  const canVisitStep = name => steps.includes(name) && (steps.indexOf(name) < 2 || terms?.accepted === true);
  const stepButtons = steps.map(name => $('setupStep' + name[0].toUpperCase() + name.slice(1)));
  const fields = () => [...$('settings').querySelectorAll('input, select')];
  const draftValues = () => JSON.stringify(fields().map(field => [field.id,field.value,field.checked]));
  const hasDraftChanges = () => entered && !application && (draftValues() !== draftBaseline || !!$('setupImportSource').value.trim());
  const confirmDiscard = () => !hasDraftChanges() || window.confirm('本轮引导的更改尚未应用。确定放弃更改并离开吗？');
  const waitingForRestart = () => application?.phase === 'restart' && !restartProblem;
  const homes = new Map();
  for (const id of ['preferences']) {
    const marker = document.createComment('home:' + id);
    $(id).before(marker); homes.set(id, marker);
  }
  function restore() {
    for (const [id, marker] of homes) marker.after($(id));
    for (const group of document.querySelectorAll('[data-setup-settings]')) group.hidden = false;
  }
  window.swdSetupOwnsSettings = () => entered;
  window.swdSetupSettingsLoaded = s => {
    $('setupCustomStorage').checked = pathKey($('storageDir').value) !== pathKey(s.defaultStorageDir);
    if ($('setupCustomStorage').checked) customPath = $('storageDir').value;
    $('setupDirect').checked = ['direct','auto'].includes($('upstreamMode').value);
    $('setupSocks5').checked = ['socks5','auto'].includes($('upstreamMode').value);
  };
  function plan() {
    return {baseVersion, storageDir:$('storageDir').value, customStorage:$('setupCustomStorage').checked,
      maxCacheBytes:Math.round(Number($('maxCacheGiB').value) * 1073741824),
      upstreamMode:$('upstreamMode').value, socks5Address:$('socks5Address').value, socks5Username:$('socks5Username').value,
      ...($('socks5Password').value || !$('socks5Username').value ? {socks5Password:$('socks5Password').value} : {}),
      importSource:$('setupImportSource').value.trim()};
  }
  function begin() {
    if (entered || !lastState || loading) return;
    original = {dirty:settingsDirty, baseline:settingsBaseline, values:fields().map(field => [field,field.value,field.checked])};
    customPath = ''; checkedPath = '';
    settingsDirty = false;
    renderSettings(lastState);
    entered = true;
    baseVersion = lastState.setupSettingsVersion;
    $('setupImportSource').value = '';
    draft = plan();
    draftBaseline = draftValues();
    application = applicationID && lastState.setupApplication?.id === applicationID ? lastState.setupApplication : null;
    if (!application) {
      applicationID = '';
      try {sessionStorage.removeItem('swd-setup-application');} catch (_) {}
    }
    setText('setupMessage','');
    if (!hostsUpdating) setText('setupHostsResult','');
    const initialStep = application ? 'finish' : step;
    if (!canVisitStep(initialStep)) step = 'welcome';
    else step = initialStep;
    mount();
    if (step !== 'terms') {
      const revision = navigationRevision;
      void loadTerms().then(() => {
        if (!entered || !isOpen() || revision !== navigationRevision || !canVisitStep(initialStep) || initialStep === step) return;
        step = initialStep; mount();
      });
    }
  }
  function discard() {
    navigationRevision++;
    termsRequest++; terms = null;
    logsRequest++;
    logsChecking = false;
    restore();
    // Keep an unresolved application identity for recovery after leaving/reload.
    const unresolved = application && ['restart','uncertain'].includes(application.phase);
    entered = false; draft = null; application = null;
    reconnecting = false; restartProblem = ''; diagnoseOnEntry = false;
    if (!unresolved) {
      applicationID = '';
      try { sessionStorage.removeItem('swd-setup-application'); } catch (_) {}
    }
    if (original?.dirty) {
      for (const [field,value,checked] of original.values) {field.value=value;field.checked=checked;}
      settingsBaseline = original.baseline; settingsDirty = true;
    } else {
      settingsDirty = false;
      if (lastState) renderSettings(lastState);
    }
    original = null;
    $('setupImportSource').value = '';
    step = 'welcome';
    renderControls();
  }
  window.swdSetupRenderControls = () => {
    renderNavigation();
    if (!isOpen() || !entered) {
      $('storageDir').setCustomValidity('');
      $('setupDirect').setCustomValidity('');
      $('upstreamMode').setCustomValidity(!$('upstreamMode').value ? '请选择连接方式。' : '');
      for (const id of ['setupCustomStorage','setupDirect','setupSocks5']) $(id).disabled = true;
      return;
    }
    const locked = applying || !!application || advancing;
    $('setupAcceptTerms').disabled = locked || terms?.accepted === true;
    // Draft editing is independent of background hosts actions and connectivity.
    for (const field of fields()) field.disabled = locked;
    for (const id of ['setupCustomStorage','setupDirect','setupSocks5','setupImportSource']) $(id).disabled = locked;
    $('storageDir').disabled ||= !$('setupCustomStorage').checked;
    for (const id of ['socks5Address','socks5Username','socks5Password']) $(id).disabled ||= $('upstreamMode').value === 'direct';
    const path = pathKey($('storageDir').value), root = pathKey(lastState?.portableDir);
    $('storageDir').setCustomValidity($('setupCustomStorage').checked && root && (path === root || path.startsWith(root + '\\')) ? '指定位置必须在 Portable 目录之外。' : '');
    $('setupDirect').setCustomValidity(!$('upstreamMode').value ? '请至少选择一种连接方式。' : '');
    $('upstreamMode').setCustomValidity('');
    $('setupHostsEnabled').disabled = hostsUpdating || !connected || !lastState;
  };
  $('setupCustomStorage').addEventListener('change', () => {
    if ($('setupCustomStorage').checked) $('storageDir').value = customPath;
    else {customPath = $('storageDir').value; $('storageDir').value = lastState?.defaultStorageDir || '';}
    $('storageDir').dispatchEvent(new Event('input', {bubbles:true}));
    if ($('setupCustomStorage').checked) $('storageDir').focus();
    void checkStorage();
  });
  for (const id of ['setupDirect','setupSocks5']) $(id).addEventListener('input', () => {
    $('upstreamMode').value = $('setupDirect').checked ? ($('setupSocks5').checked ? 'auto' : 'direct') : ($('setupSocks5').checked ? 'socks5' : '');
  });
  async function loadTerms() {
    const request = ++termsRequest;
    terms = null;
    setText('setupTermsText','正在读取条款…');
    try {
      const response = await fetch('/api/terms/current', {signal:AbortSignal.timeout(10000)});
      if (!response.ok) throw new Error('读取失败');
      const current = await response.json();
      if (request !== termsRequest) return;
      terms = current;
      setText('setupTermsText', terms.text);
      setText('setupMessage','');
      if (entered && isOpen() && terms.accepted === true && diagnoseOnEntry) {
        diagnoseOnEntry = false;
        if (!application && !applicationID) void checkNetwork();
      }
    } catch (_) {
      if (request === termsRequest) setText('setupTermsText', '条款暂时无法载入，请重试或使用下方链接阅读完整文本。');
    } finally {if (request === termsRequest) {renderControls(); update();}}
  }
  async function acceptTerms() {
    if (!terms) throw new Error('条款尚未载入，请重试。');
    if (terms.accepted) return;
    const current = terms;
    const response = await fetch('/api/terms/accept', {method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':token},
      body:JSON.stringify({version:current.version,sha256:current.sha256,agree:true,contentRights:true}),signal:AbortSignal.timeout(15000)});
    if (!response.ok) throw new Error(await response.text());
    current.accepted = true;
    // The server starts diagnosis only after the CurrentUser receipt is durable.
    void requestRefresh();
  }
  async function confirmTerms() {
    if (!entered || loading || advancing || applying || application) return;
    if (!terms) {
      setText('setupMessage','尚未读到条款及确认记录，正在重试。');
      void loadTerms();
      return;
    }
    if (terms.accepted === true) return;
    const revision = navigationRevision;
    advancing = true; renderControls();
    try {
      await acceptTerms();
      diagnoseOnEntry = false;
    } catch (error) {
      if (isOpen() && revision === navigationRevision) setText('setupMessage','条款确认未保存：' + error.message);
      return;
    } finally {advancing = false; renderControls(); update();}
  }
  $('setupAcceptTerms').onclick = confirmTerms;
  async function checkLogs() {
    const request = ++logsRequest;
    logsChecking = true;
    $('setupCheckLogs').disabled = true;
    setText('setupLogsStatus','正在检测 VRC 日志…');
    setText('setupLogsPath','');
    try {
      const result = await post('logs',{});
      if (request !== logsRequest) return;
      setText('setupLogsStatus',result.message);
      setText('setupLogsPath',result.path || result.directory);
    } catch (error) {
      if (request === logsRequest) setText('setupLogsStatus','无法确认：' + error.message + '。请重试。');
    } finally {if (request === logsRequest) {logsChecking = false; renderNavigation();}}
  }
  $('setupCheckLogs').onclick = checkLogs;
  let networkStarting = false;
  async function checkNetwork() {
    if (networkStarting) return;
    networkStarting = true;
    setText('setupDirectStatus', '正在启动直连诊断…');
    try {
      const response = await fetch('/api/setup/network', {method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':token},body:'{}',signal:AbortSignal.timeout(15000)});
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || '诊断未能启动');
      await requestRefresh();
    } catch (error) { setText('setupDirectStatus', '直连诊断启动结果未确认：' + error.message + '。请查看状态后重试。'); }
    finally { networkStarting = false; }
  }
  $('setupRetryNetwork').onclick = checkNetwork;
  async function checkStorage() {
    const path = $('storageDir').value.trim();
    const request = ++storageRequest;
    checkedPath = path;
    setText('setupDiskSpace', path ? '正在读取分区容量…' : '填写位置后显示分区容量');
    if (!path) return;
    setText('setupStorageStatus', '正在检查所选目录…');
    try {
      const response = await fetch('/api/setup/storage', {method:'POST', headers:{'Content-Type':'application/json','X-SWD-Token':token}, body:JSON.stringify({path}), signal:AbortSignal.timeout(15000)});
      const result = await response.json();
      if (request !== storageRequest || path !== $('storageDir').value.trim()) return;
      if (!response.ok) throw new Error(result.error || '目录检查失败');
      setText('setupDiskSpace', result.space.state === 'ok' ? '分区总容量 ' + (result.totalBytes / 2**30).toFixed(2) + ' GiB · 可用 ' + (result.freeBytes / 2**30).toFixed(2) + ' GiB' : result.space.message);
      setText('setupStorageStatus', [result.writable,result.library].map(v => (v.state === 'failed' ? '待处理：' : v.state === 'unknown' ? '未知：' : '') + v.message).join('\n'));
    } catch (error) {
      if (request === storageRequest) {
        setText('setupStorageStatus', '无法确认：' + error.message + '。可重试或稍后检查。');
        setText('setupDiskSpace', '分区容量暂时无法读取');
      }
    }
  }
  $('setupCheckStorage').onclick = checkStorage;
  $('storageDir').addEventListener('input', () => {
    storageRequest++;
    checkedPath = '';
    setText('setupStorageStatus', '目录已改变，完成填写后检查。');
    setText('setupDiskSpace', '目录已改变，等待检查');
  });
  $('storageDir').addEventListener('change', checkStorage);
  async function post(path, body, timeout = 15000) {
    const response = await fetch('/api/setup/' + path, {method:'POST', headers:{'Content-Type':'application/json','X-SWD-Token':token},body:JSON.stringify(body),signal:AbortSignal.timeout(timeout)});
    const data = await response.json();
    if (!response.ok) {
      const error = new Error(data.error || '请求失败');
      error.rejected = true;
      throw error;
    }
    return data;
  }
  function persist(state = 'active') {
    // Navigation metadata only: it never contains draft settings or secrets.
    const body = JSON.stringify({schemaVersion:1,step,state});
    progressWrites = progressWrites.then(() => fetch('/api/setup', {method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':token},body,signal:AbortSignal.timeout(10000)})).catch(() => {});
    return progressWrites;
  }
  function mount() {
    restore();
    for (const panel of document.querySelectorAll('[data-setup-step]')) panel.hidden = panel.dataset.setupStep !== step;
    if (['storage','proxy'].includes(step)) {
      $('setup' + step[0].toUpperCase() + step.slice(1) + 'Slot').append($('preferences'));
      for (const group of document.querySelectorAll('[data-setup-settings]')) group.hidden = group.dataset.setupSettings !== step;
    }
    if (step === 'terms') void loadTerms();
    if (step === 'environment') void checkLogs();
    setText('setupTitle', titles[steps.indexOf(step)]);
    setText('setupPosition', (steps.indexOf(step) + 1) + ' / ' + steps.length);
    renderControls(); update();
    document.dispatchEvent(new CustomEvent('setupstep', {detail:step}));
  }
  function validPage() {
    if (!['storage','proxy'].includes(step)) return true;
    const invalid = fields().find(field => field.closest('[data-setup-settings]')?.dataset.setupSettings === step && !field.closest('.console-only, .setup-storage-extra') && !field.disabled && !field.checkValidity());
    if (invalid) {invalid.reportValidity(); return false;}
    return true;
  }
  async function go(next) {
    if (!entered || loading || advancing || applying || application) return;
    if (!canVisitStep(next)) {
      setText('setupMessage','需要已保存的有效条款确认记录，才能进入后续步骤。');
      return;
    }
    if (!validPage()) return;
    const revision = ++navigationRevision;
    advancing = true; renderControls();
    try {
      const candidate = plan();
      if (['storage','proxy','import'].includes(step)) await post('validate', {...candidate,page:step});
      if (!isOpen() || revision !== navigationRevision) return;
      draft = candidate;
      setText('setupMessage','');
      step = next; void persist(); mount(); $('setupTitle').focus();
    } catch (error) {
      if (isOpen() && revision === navigationRevision) setText('setupMessage', '本页尚未通过检查：' + error.message);
    } finally {advancing = false; renderControls();}
  }
  function defaults() {
    if (step === 'storage') return {storageDir:lastState?.defaultStorageDir || '',maxCacheGiB:'0'};
    if (step === 'proxy') return {upstreamMode:'direct',socks5Address:'',socks5Username:'',socks5Password:''};
    if (step === 'import') return {setupImportSource:''};
    return null;
  }
  function atDefaults() {
    if (step === 'storage' && $('setupCustomStorage').checked) return false;
    const values = defaults();
    return !values || Object.entries(values).every(([id,value]) => id === 'storageDir' ? pathKey($(id).value) === pathKey(value)
      : id === 'maxCacheGiB' ? $(id).value !== '' && Number($(id).value) === 0 : $(id).value === value);
  }
  function renderNavigation() {
    $('setupCheckLogs').disabled = logsChecking || !connected;
    const locked = loading || !entered || advancing || applying || !!application;
    stepButtons.forEach((button,index) => {
      button.disabled = locked || !canVisitStep(steps[index]);
      if (steps[index] === step) button.setAttribute('aria-current','step'); else button.removeAttribute('aria-current');
    });
    $('setupBack').disabled = locked || step === 'welcome';
    $('setupNext').disabled = loading || !entered || advancing || applying || waitingForRestart() || application?.phase === 'importing';
    $('setupNext').disabled ||= step === 'finish' && !connected && !restartProblem;
    $('setupNext').disabled ||= step === 'terms' && !canVisitStep('storage');
    $('setupNext').textContent = restartProblem ? '重新连接并核对' : application?.phase === 'uncertain' ? '核对应用结果' : application ? '返回控制台' : step === 'finish' ? '确认并应用' : '下一步';
    $('setupLeave').disabled = applying || waitingForRestart() || application?.phase === 'importing';
    $('setupLeave').textContent = application ? '返回控制台' : '放弃更改并退出引导';
    $('setupChangeStorage').disabled = locked || !canVisitStep('storage');
    $('setupReset').hidden = !defaults() || !!application;
    $('setupReset').disabled = locked || atDefaults();
    $('setupAcceptTerms').hidden = step !== 'terms';
    $('setupAcceptTerms').textContent = terms?.accepted === true ? '已确认条款' : terms ? '确认同意条款' : '重新读取条款';
  }
  function renderSummary(s) {
    const p = {...(draft || plan())}, current = s.activeSettings || s.settings;
    if (application?.importSource) p.importSource = application.importSource;
    const mode = value => ({direct:'直连',socks5:'仅 SOCKS5',auto:'直连 + SOCKS5'}[value] || '未选择');
    const capacity = bytes => bytes ? (bytes / 1073741824) + ' GiB' : '不限';
    const change = (label,before,after) => label + '：' + (before === after ? after + '（保持）' : before + ' → ' + after);
    const lines = [change('上游',mode(current.upstreamMode),mode(p.upstreamMode))];
    if (p.upstreamMode !== 'direct') {
      lines.push(change('SOCKS5',current.socks5Address || '未设置',p.socks5Address || '未设置'));
      lines.push(change('认证用户',current.socks5Username || '无',p.socks5Username || '无'));
      if (p.socks5Password) lines.push('代理密码：将更新');
      else if (p.socks5Password === '' && s.socks5PasswordSet) lines.push('代理密码：将清除');
    }
    lines.push(change('存储',current.storageDir,p.storageDir),change('缓存上限',capacity(current.maxCacheBytes),capacity(p.maxCacheBytes)));
    lines.push(p.importSource ? '导入：' + p.importSource + '\n目标：' + p.storageDir + ' · 保留源文件' : '导入：不执行');
    // Previously saved advanced changes are also activated by the restart.
    for (const [key,label] of [['autoStartCDN','自动启动缓存服务'],['autoStartQueue','自动启动队列'],['queuePrefetchCount','队列预缓存数'],['requestRetentionDays','请求保留天数'],['throughputIntervalMinutes','测速间隔（分钟）'],['downloadUpstream','回源线路'],['manualLogDir','手动日志位置'],['logDir','日志目录']]) {
      if (current[key] !== s.settings[key]) lines.push(change(label,String(current[key]),String(s.settings[key])) + '（此前已保存）');
    }
    setText('setupSummary',lines.join('\n'));
    setText('setupApplyResult',application ? (application.configSaved ? '配置已保存。' : '') + (restartProblem || (application.phase === 'restart' ? '正在重启，之后执行导入…' : application.phase === 'completed' ? (application.importStarted ? '设置已应用，导入已启动；进度可在设置页查看。' : '清单已应用。') : application.error || '正在应用…')) : '');
    $('setupApplyHint').hidden = !!application;
  }
  function update() {
    if (isOpen() && !entered) begin();
    const s = lastState;
    if (!s) {renderNavigation(); return;}
    if (applicationID && s.setupApplication?.id === applicationID) application = s.setupApplication;
    if (application?.phase === 'restart' && !reconnecting && !restartProblem) void recoverRestart();
    renderNavigation();
    if (isOpen() && step === 'storage' && checkedPath !== $('storageDir').value.trim()) void checkStorage();
    const diagnostic = s.setupNetwork || {}, health = diagnostic.monitor || {};
    const status = diagnostic.running ? '正在测速，可继续下一步。' : diagnostic.error ? '未完成：' + diagnostic.error : health.finished && !health.finished.startsWith('0001') ? '检测完成 · ' + new Date(health.finished).toLocaleString() : '尚未检测';
    if (!networkStarting) setText('setupDirectStatus',status);
    setText('setupNetworkBackground',status);
    $('setupRetryNetwork').disabled = networkStarting || !!diagnostic.running;
    const minutes = (health.policy?.throughputInterval || 20 * 60 * 1e9) / (60 * 1e9);
    const speeds = (health.results || []).filter(r => r.operation === 'resource' && r.state === 'available' && Number.isFinite(r.estimatedSpeedBPS) && r.estimatedSpeedBPS > 0 && Date.now() < new Date(r.throughputObservedAt).getTime() + minutes * 60000).map(r => r.estimatedSpeedBPS);
    setText('setupDirectAdvice',(speeds.length ? '测速样本最高 ' + (Math.max(...speeds) / 1048576).toFixed(2) + ' MiB/s。' : '') + healthImpact(health,{upstreamMode:'direct'}));
    const results = health.results || [];
    renderStableList('setupDirectResults',results,r => r.operation + '/' + r.route + '/' + r.channelID,r => {
      const row = renderHealthResult(r,minutes,diagnostic.running ? '正在测量' : '等待重试');
      row.prepend(recentElement('p','',({catalog:'清单',playback_url:'播放地址',resource:'视频资源'}[r.operation] || r.operation) + ' · ' + r.route)); return row;
    },results.map(r => !!r.lastThroughput && Date.now() >= new Date(r.lastThroughput.observedAt).getTime() + minutes * 60000));
    setText('setupTermsState',terms ? terms.accepted ? '已保存本版条款确认。' : '确认后立即保存、开始后台测速，解锁后续步骤。' : '尚未读到确认记录');
    setText('setupSaveState',application ? '' : !connected ? '草稿留在本轮引导中 · 重连后才能校验和应用' : step === 'terms' ? (terms?.accepted ? '已保存条款确认' : '欢迎与条款可自由切换') + ' · 设置仍在最后应用' : '仅保存本轮草稿 · 最后确认才应用');
    setText('setupImportTarget','确认后导入到：' + $('storageDir').value);
    setText('setupHosts',s.hosts.message || (s.hosts.ready ? '映射已启用。' : s.hosts.conflict ? '存在冲突，请处理后重试。' : '映射未启用。'));
    if (!hostsUpdating) $('setupHostsEnabled').checked = !!s.hosts.ready;
    $('setupHostsEnabled').disabled = hostsUpdating || !connected;
    const portText = (ok,owner) => ok ? (s.running ? '本服务已监听' : '空闲，可用') : owner ? '被 ' + owner.name + '（PID ' + owner.pid + '）占用' : '被占用或不可用';
    setText('setupPort80','HTTP · 80：' + portText(s.portOK,s.portOwner));
    setText('setupPort443','HTTPS · 443：' + portText(s.httpsPortOK,s.httpsPortOwner));
    if (entered) renderSummary(s);
  }
  async function recoverRestart() {
    if (reconnecting) return;
    reconnecting = true; restartProblem = '';
    renderControls(); update();
    const id = applicationID;
    try {
      const result = await reconnectAfterRestart();
      if (result.state === 'reloading') return;
      if (entered && applicationID === id) restartProblem = result.message;
    } catch (_) {
      if (entered && applicationID === id) restartProblem = '重连未完成。请重新连接并核对，或刷新页面；仍无法连接时请手动启动应用。';
    }
    reconnecting = false;
    renderControls(); update();
  }
  async function apply() {
    if (applying || !entered || application || !connected || !canVisitStep('finish')) return;
    applying = true; renderControls();
    applicationID ||= crypto.randomUUID();
    try {sessionStorage.setItem('swd-setup-application',applicationID);} catch (_) {}
    try {
      await progressWrites;
      const result = await post('apply',{...draft,id:applicationID},30000);
      application = result;
      settingsDirty = false;
      if (result.phase === 'restart') {
        await recoverRestart();
      } else await requestRefresh();
    } catch (error) {
      if (error.rejected) {
        setText('setupMessage','尚未应用：' + error.message);
        applicationID = '';
        try {sessionStorage.removeItem('swd-setup-application');} catch (_) {}
      } else {
        application = {id:applicationID,phase:'uncertain',error:'结果尚未确认，请核对应用结果。'};
        setText('setupMessage','应用结果尚未确认：' + error.message + '。正在核对，请勿重复创建清单。');
      }
      await requestRefresh();
      if (lastState?.setupApplication?.id === applicationID) application = lastState.setupApplication;
      // Keep the same id for retries after an uncertain response.
    } finally {applying = false; renderControls(); update();}
  }
  $('setupHostsEnabled').addEventListener('change',async () => {
    if (hostsUpdating) return;
    const desired = $('setupHostsEnabled').checked;
    hostsUpdating = true; update();
    setText('setupHostsResult','正在' + (desired ? '启用' : '恢复') + ' hosts；如出现 UAC，请确认系统提示。');
    try {
      const response = await fetch('/api/hosts/' + (desired ? 'enable' : 'disable'),{method:'POST',headers:{'X-SWD-Token':token,'Content-Type':'application/json'},body:'{}',signal:AbortSignal.timeout(120000)});
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || '操作失败');
      setText('setupHostsResult',desired ? '已启用 hosts 映射。' : '已恢复 hosts。');
    } catch (error) {setText('setupHostsResult','hosts 操作未确认：' + error.message + '。请核对实际状态后重试。');}
    finally {await requestRefresh(); hostsUpdating = false; update();}
  });
  const exit = (destination, explicitDiscard = false) => {
    if (applying || waitingForRestart() || (!explicitDiscard && !confirmDiscard())) return;
    void persist(application?.phase === 'completed' ? 'completed' : 'skipped'); discard();
    if (destination.startsWith('#')) window.location.hash = destination; else window.location.assign(destination);
  };
  stepButtons.forEach((button,index) => {button.onclick = () => steps[index] !== step && go(steps[index]);});
  $('setupBack').onclick = () => go(steps[Math.max(0,steps.indexOf(step) - 1)]);
  $('setupNext').onclick = () => {
    if (restartProblem) return recoverRestart();
    if (application?.phase === 'uncertain') {application = null; return apply();}
    return application ? exit('#/home') : step === 'finish' ? apply() : go(steps[steps.indexOf(step) + 1]);
  };
  $('setupLeave').onclick = () => exit('#/home',true);
  $('setupChangeStorage').onclick = () => go('storage');
  window.swdSetupSubmit = () => {if (!isOpen()) return false; void $('setupNext').onclick(); return true;};
  $('setupReset').onclick = () => {
    if ($('setupReset').disabled) return;
    for (const [id,value] of Object.entries(defaults() || {})) $(id).value = value;
    if (step === 'storage') {customPath = ''; $('setupCustomStorage').checked = false;}
    if (step === 'proxy') {$('setupDirect').checked = true; $('setupSocks5').checked = false;}
    $('settings').dispatchEvent(new Event('input',{bubbles:true}));
    renderControls(); update();
    if (step === 'storage') void checkStorage();
  };
  for (const link of document.querySelectorAll('#page-setup a[href]')) {
    const href = link.getAttribute('href');
    if (href.startsWith('#') || link.getAttribute('target') === '_blank') continue;
    link.addEventListener('click',event => {event.preventDefault(); exit(href);});
  }
  window.swdBeforeNavigate = (from,to) => {
    if (from === 'setup' && to !== 'setup') {
      if (applying || waitingForRestart() || !confirmDiscard()) return false;
      if (entered) {void persist(application?.phase === 'completed' ? 'completed' : 'skipped'); discard();}
    }
    if (from !== 'setup' && to === 'setup') diagnoseOnEntry = true;
    return true;
  };
  document.addEventListener('pagechange',() => {
    navigationRevision++;
    if (isOpen()) begin(); else {restore(); renderControls();}
  });
  document.addEventListener('consolestate',update);
  for (const name of ['input','change']) $('settings').addEventListener(name,update);
  $('setupImportSource').addEventListener('input',renderNavigation);
  window.addEventListener('beforeunload',event => {
    if (!exiting && hasDraftChanges()) {event.preventDefault(); event.returnValue = '';}
  });
  (async () => {
    try {
      const response = await fetch('/api/setup',{signal:AbortSignal.timeout(10000)});
      if (!response.ok) throw new Error('无法读取上次引导进度；可以重新开始。');
      const progress = await response.json();
      if (steps.includes(progress.step) && progress.state === 'active') step = progress.step;
      if (!window.swdInitialRoute && window.location.hash === '#/home' && progress.state === 'active') window.location.hash = '#/setup';
    } catch (error) {setText('setupMessage',error.message);}
    finally {loading = false; if (isOpen()) begin(); else update();}
  })();
})();
