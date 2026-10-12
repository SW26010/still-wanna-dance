// The wizard owns a per-visit draft. Navigation validates it but never writes
// business settings. Only the review page can approve an application.
(() => {
  const steps = ['welcome','storage','proxy','finish'];
  const titles = ['开始','存储','网络','启用'];
  const currentStep = name => ({terms:'welcome',import:'storage',direct:'proxy',environment:'finish'}[name] || name);
  const visited = new Set();
  let step = 'welcome', loading = true, entered = false, advancing = false;
  let navigationRevision = 0, storageRequest = 0, checkedPath = '', terms = null;
  let storagePendingPath = null, storageSpacePath = '';
  let termsRequest = 0, termsPrompted = false;
  let logsRequest = 0, logsChecking = false, accessChecking = false;
  let logsState = '', folderController = null, folderRevision = 0;
  let draft = null, baseVersion = '', original = null, applying = false, application = null, hostsUpdating = false;
  let reconnecting = false, restartProblem = '', draftBaseline = '';
  let checkingReceipt = false, receiptRequest = 0;
  const firstEntry = window.swdTermsGate;
  let startingSession = !!firstEntry, beforeConsentBusy = false;
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
  const canVisitStep = name => steps.includes(name) && (name === 'welcome' || (terms?.accepted === true && !startingSession));
  const stepButtons = steps.map(name => $('setupStep' + name[0].toUpperCase() + name.slice(1)));
  const fields = () => [...$('settings').querySelectorAll('input, select, textarea')].filter(field => field.type !== 'file');
  const draftValues = () => JSON.stringify([fields().map(field => [field.id,field.value,field.checked]),$('setupCapacityLimited').checked,$('setupImportEnabled').checked]);
  const hasDraftChanges = () => entered && !!draft && !application && (draftValues() !== draftBaseline || !!$('setupImportSource').value.trim());
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
    $('storageDir').readOnly = false;
  }
  window.swdSetupOwnsSettings = () => entered;
  window.swdSetupSettingsLoaded = s => {
    $('setupCustomStorage').checked = pathKey($('storageDir').value) !== pathKey(s.defaultStorageDir);
    $('setupDirect').checked = ['direct','auto'].includes($('upstreamMode').value);
    $('setupSocks5').checked = ['proxy','socks5','auto'].includes($('upstreamMode').value);
    syncCapacity();
  };
  function syncCapacity() {
    const limited = Number($('maxCacheGiB').value) > 0;
    $('setupCapacityLimited').checked = limited;
    $('setupCapacityUnlimited').checked = !limited;
    $('setupCapacityValue').value = limited ? $('maxCacheGiB').value : '40';
  }
  function plan() {
    return {baseVersion, storageDir:$('storageDir').value, customStorage:$('setupCustomStorage').checked,
      maxCacheBytes:Math.round(Number($('maxCacheGiB').value) * 1073741824),
      upstreamMode:$('upstreamMode').value, socks5Address:$('socks5Address').value, socks5Username:$('socks5Username').value,
      proxyBackend:$('proxyBackend').value || 'external-socks5', proxyImportID:$('proxyImportID').value,
      ...($('socks5Password').value || !$('socks5Username').value ? {socks5Password:$('socks5Password').value} : {}),
      importSource:$('setupImportSource').value.trim()};
  }
  function initializeDraft() {
    original = {dirty:settingsDirty, baseline:settingsBaseline, values:fields().map(field => [field,field.value,field.checked])};
    globalThis.swdResetProxyImport?.({preserve:true}); // Restore this draft on discard.
    checkedPath = '';
    settingsDirty = false;
    // The start panel stays mounted while the first authenticated
    // state arrives. Only initialize the fields that were inaccessible before it.
    const wasEntered = entered; entered = false;
    renderSettings(lastState);
    entered = wasEntered;
    baseVersion = lastState.setupSettingsVersion;
    $('setupImportSource').value = '';
    $('setupImportEnabled').checked = false;
    draft = plan();
    draftBaseline = draftValues();
    application = applicationID && lastState.setupApplication?.id === applicationID ? lastState.setupApplication : null;
    if (!application && applicationID) application = {id:applicationID,phase:'uncertain',error:'正在按原应用 ID 核对回执…'};
  }
  function begin() {
    if (entered || loading || (!lastState && !firstEntry)) return;
    if (!startingSession) initializeDraft();
    entered = true;
    visited.clear();
    $('setupRepair').open = false;
    setText('setupMessage','');
    if (!hostsUpdating) setText('setupHostsResult','');
    const initialStep = application ? 'finish' : step;
    if (!canVisitStep(initialStep)) step = 'welcome';
    else step = initialStep;
    mount();
    if (!entered) return; // A recovered completed receipt already left the wizard.
    if (application?.phase === 'uncertain') void reconcileApplication();
    {
      const revision = navigationRevision;
      void loadTerms().then(() => {
        const destination = application ? 'finish' : initialStep;
        if (!entered || !isOpen() || revision !== navigationRevision || !canVisitStep(destination) || destination === step) return;
        step = destination; mount();
      });
    }
  }
  async function startSession() {
    if (!startingSession || terms?.accepted !== true) return;
    // The server's durable receipt authorizes API reads, never a navigation hint.
    window.swdTermsGate = null;
    diagnoseOnEntry = false;
    await requestRefresh();
    if (startingSession) setText('setupMessage','条款已确认，正在连接设置；连接恢复后可继续。');
  }
  function discard() {
    navigationRevision++;
    globalThis.swdResetProxyImport?.();
    $('setupTermsDialog').close();
    cancelFolder();
    termsRequest++; terms = null;
    termsPrompted = false;
    receiptRequest++; checkingReceipt = false;
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
    $('setupImportEnabled').checked = false;
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
    const locked = applying || !!application || advancing || !!globalThis.swdProxyImportPending?.();
    for (const id of ['setupShowTerms','setupCloseTerms']) $(id).disabled = false;
    $('setupRestoreHelp').disabled = beforeConsentBusy || hostsUpdating;
    $('setupAcceptTerms').disabled = locked || terms?.accepted === true;
    // Draft editing is independent of background hosts actions and connectivity.
    for (const field of fields()) field.disabled = locked || startingSession;
    for (const id of ['setupCustomStorage','setupDirect','setupSocks5','setupImportSource']) $(id).disabled = locked || startingSession;
    for (const id of ['setupCapacityUnlimited','setupCapacityLimited','setupCapacityValue','setupImportEnabled']) $(id).disabled = locked || startingSession;
    $('setupCapacityValue').disabled ||= !$('setupCapacityLimited').checked;
    for (const id of ['setupStoragePath','setupImportSelection']) $(id).disabled = locked || startingSession || !!folderController || !connected;
    $('setupImportSelection').disabled ||= !$('setupImportEnabled').checked;
    $('setupDefaultStorage').disabled = locked || startingSession || pathKey($('storageDir').value) === pathKey(lastState?.defaultStorageDir);
    $('setupCheckStorage').disabled = locked || startingSession || storagePendingPath !== null || !connected;
    $('storageDir').readOnly = true;
    $('storageDir').disabled ||= !$('setupCustomStorage').checked;
    for (const id of ['socks5Address','socks5Username','socks5Password']) $(id).disabled ||= $('upstreamMode').value === 'direct';
    globalThis.swdRenderProxyControls?.(locked || startingSession || !connected);
    const path = pathKey($('storageDir').value), root = pathKey(lastState?.portableDir);
    $('storageDir').setCustomValidity($('setupCustomStorage').checked && root && (path === root || path.startsWith(root + '\\')) ? '指定位置必须在程序文件夹之外。' : '');
    $('setupDirect').setCustomValidity(!$('upstreamMode').value ? '请至少选择一种连接方式。' : '');
    $('upstreamMode').setCustomValidity('');
    renderHostsControls();
  };
  function setStorage(path) {
    $('storageDir').value = path;
    $('setupCustomStorage').checked = pathKey(path) !== pathKey(lastState?.defaultStorageDir);
    $('storageDir').dispatchEvent(new Event('input', {bubbles:true}));
    $('settings').dispatchEvent(new Event('input',{bubbles:true}));
    void checkStorage();
  }
  function folderFeedback(kind, message = '') {
    const id = kind === 'storage' ? 'setupStorage' : 'setupImport';
    setText(id + 'FeedbackText',message);
    $(id + 'Feedback').hidden = !message;
    $(kind === 'storage' ? 'setupStoragePath' : 'setupImportSelection').setAttribute('aria-invalid',String(!!message));
  }
  function cancelFolder() {
    folderRevision++;folderController?.abort();folderController = null;
    for (const kind of ['storage','import']) folderFeedback(kind);
    for (const id of ['setupStoragePath','setupImportSelection']) $(id).removeAttribute('aria-busy');
  }
  async function chooseFolder(kind) {
    if (folderController || !entered || application || advancing || (kind === 'import' && !$('setupImportEnabled').checked)) return;
    const request = ++folderRevision, revision = navigationRevision;
    const controller = folderController = new AbortController();
    const input = $(kind === 'storage' ? 'setupStoragePath' : 'setupImportSelection');
    folderFeedback(kind);input.setAttribute('aria-busy','true');renderControls();
    try {
      const result = await post('folder',{kind,path:kind === 'storage' ? $('storageDir').value : $('setupImportSource').value},300000,AbortSignal.any([controller.signal,AbortSignal.timeout(300000)]));
      if (request !== folderRevision || revision !== navigationRevision || !entered) return;
      if (!result.canceled && result.path) {
        if (kind === 'storage') setStorage(result.path);
        else {$('setupImportSource').value = result.path; update();}
      }
    } catch (error) {
      if (request === folderRevision && entered && !controller.signal.aborted) folderFeedback(kind,'未选择文件夹：' + error.message + '。请重试。');
    } finally {if (request === folderRevision) {folderController = null; input.removeAttribute('aria-busy');renderControls(); update();}}
  }
  $('setupStoragePath').onclick = () => chooseFolder('storage');
  $('setupStoragePath').addEventListener('keydown',event => {if (event.key === 'Enter' || event.key === ' ') {event.preventDefault();void chooseFolder('storage');}});
  $('setupImportSelection').onclick = () => chooseFolder('import');
  $('setupImportSelection').addEventListener('keydown',event => {if (event.key === 'Enter' || event.key === ' ') {event.preventDefault();void chooseFolder('import');}});
  $('setupDefaultStorage').onclick = () => {cancelFolder();setStorage(lastState.defaultStorageDir);};
  $('setupImportEnabled').onchange = () => {
    if (!$('setupImportEnabled').checked) {cancelFolder();$('setupImportSource').value = '';}
    folderFeedback('import');renderControls();update();
  };
  function openTerms() {
    if (!$('setupTermsDialog').open) $('setupTermsDialog').showModal();
  }
  $('setupShowTerms').onclick = () => {openTerms();if (!advancing) return loadTerms();};
  $('setupCloseTerms').onclick = () => $('setupTermsDialog').close();
  for (const id of ['setupCapacityUnlimited','setupCapacityLimited','setupCapacityValue']) $(id).addEventListener('input',() => {
    $('maxCacheGiB').value = $('setupCapacityLimited').checked ? $('setupCapacityValue').value : '0';
    $('settings').dispatchEvent(new Event('input',{bubbles:true}));
  });
  for (const id of ['setupDirect','setupSocks5']) $(id).addEventListener('input', () => {
    $('upstreamMode').value = $('setupDirect').checked ? ($('setupSocks5').checked ? 'auto' : 'direct') : ($('setupSocks5').checked ? 'proxy' : '');
  });
  async function loadTerms() {
    const request = ++termsRequest;
    const wasAccepted = terms?.accepted === true;
    terms = null;
    setText('setupTermsText','正在读取条款…');
    try {
      const response = await fetch('/api/terms/current', {signal:AbortSignal.timeout(10000)});
      if (!response.ok) throw new Error('读取失败');
      const current = await response.json();
      if (request !== termsRequest) return;
      terms = current;
      setText('setupTermsText', terms.text);
      const reconfirm = !terms.accepted && (terms.needsConfirmation || wasAccepted);
      $('setupTermsNotice').hidden = !reconfirm;
      setText('setupTermsNotice',reconfirm ? '之前的同意记录无法验证，请重新阅读并确认。' : '');
      if (reconfirm && !termsPrompted) {termsPrompted = true;openTerms();}
      setText('setupTermsResult','');
      setText('setupMessage','');
      if (startingSession && terms.accepted === true) void startSession();
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
    if (startingSession) await startSession();
    else void requestRefresh();
    void persist();
  }
  async function confirmTerms() {
    if (!entered || loading || advancing || applying || application) return;
    if (!terms) {
      setText('setupMessage','尚未读到条款及确认记录，正在重试。');
      await loadTerms();
      return;
    }
    if (terms.accepted === true) return;
    const revision = navigationRevision;
    setText('setupTermsResult','');
    advancing = true; renderControls();
    try {
      await acceptTerms();
      diagnoseOnEntry = false;
      termsPrompted = false;
      $('setupTermsDialog').close();
    } catch (error) {
      if (isOpen() && revision === navigationRevision) {
        setText('setupMessage','条款确认未保存：' + error.message);
        setText('setupTermsResult','条款确认未保存：' + error.message);
      }
      return;
    } finally {advancing = false; renderControls(); update();}
  }
  $('setupAcceptTerms').onclick = confirmTerms;
  async function checkLogs() {
    const request = ++logsRequest;
    logsChecking = true;
    $('setupCheckAccess').disabled = true;
    setText('setupLogsStatus','正在检测 VRC 日志…');
    setText('setupLogsPath','');
    try {
      const result = await post('logs',{});
      if (request !== logsRequest) return;
      logsState = result.state;
      setText('setupLogsStatus',result.message);
      setText('setupLogsPath',result.directory || result.path || '未定位');
    } catch (error) {
      if (request === logsRequest) {logsState = 'unknown';setText('setupLogsStatus','无法确认：' + error.message + '。请重试。');}
    } finally {if (request === logsRequest) {logsChecking = false; renderNavigation(); update();}}
  }
  $('setupCheckAccess').onclick = async () => {
    if (accessChecking || logsChecking || !connected) return;
    const revision = navigationRevision;
    accessChecking = true;renderNavigation();
    try {
      await requestRefresh();
      if (entered && revision === navigationRevision && connected) await checkLogs();
    } finally {accessChecking = false;renderNavigation();}
  };
  let networkStarting = false, networkStartError = '';
  async function checkNetwork(kind = '') {
    if (networkStarting) return;
    networkStarting = true; networkStartError = ''; update();
    try {
      const response = await fetch('/api/setup/network' + (kind ? '/' + kind : ''), {method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':token},body:'{}',signal:AbortSignal.timeout(15000)});
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || '诊断未能启动');
      await requestRefresh();
    } catch (error) { networkStartError = '直连诊断启动结果未确认：' + error.message + '。请重试。'; }
    finally { networkStarting = false; update(); }
  }
  $('setupRetryNetwork').onclick = () => checkNetwork();
  async function checkStorage() {
    const path = $('storageDir').value.trim();
    if (storagePendingPath === path) return;
    const request = ++storageRequest;
    checkedPath = path;
    if (storageSpacePath !== path) setText('setupDiskSpace',path ? '（容量待确认）' : '');
    if (!path) return;
    storagePendingPath = path;
    $('setupDiskSpace').setAttribute('aria-busy','true');
    $('setupCheckStorage').disabled = true;
    try {
      const response = await fetch('/api/setup/storage', {method:'POST', headers:{'Content-Type':'application/json','X-SWD-Token':token}, body:JSON.stringify({path}), signal:AbortSignal.timeout(15000)});
      const result = await response.json();
      if (request !== storageRequest || path !== $('storageDir').value.trim()) return;
      if (!response.ok) throw new Error(result.error || '目录检查失败');
      const giB = bytes => Number((bytes / 2**30).toFixed(1));
      storageSpacePath = path;
      setText('setupDiskSpace', result.space.state === 'ok' ? '（可用 ' + giB(result.freeBytes) + ' / ' + giB(result.totalBytes) + ' GiB）' : result.space.message);
      const issues = [result.writable,result.library].filter(v => v.state !== 'ok');
      setText('setupStorageStatus', issues.map(v => v.message).join('\n'));
      $('setupCheckStorage').hidden = issues.length === 0;
    } catch (error) {
      if (request === storageRequest) {
        setText('setupStorageStatus', '无法确认：' + error.message + '。可重试或稍后检查。');
        setText('setupDiskSpace', '分区容量暂时无法读取');
        $('setupCheckStorage').hidden = false;
      }
    } finally {
      if (request === storageRequest) {
        storagePendingPath = null;
        $('setupDiskSpace').removeAttribute('aria-busy');
        $('setupCheckStorage').disabled = false;
      }
    }
  }
  $('setupCheckStorage').onclick = checkStorage;
  $('storageDir').addEventListener('input', () => {
    storageRequest++;
    storagePendingPath = null;
    checkedPath = '';
    setText('setupStorageStatus','');
    $('setupCheckStorage').hidden = true;
  });
  $('storageDir').addEventListener('change', checkStorage);
  async function post(path, body, timeout = 15000, signal) {
    const response = await fetch('/api/setup/' + path, {method:'POST', headers:{'Content-Type':'application/json','X-SWD-Token':token},body:JSON.stringify(body),signal:signal || AbortSignal.timeout(timeout)});
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
    if (window.swdTermsGate) return Promise.resolve();
    const body = JSON.stringify({schemaVersion:1,step,state});
    progressWrites = progressWrites.then(() => fetch('/api/setup', {method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':token},body,signal:AbortSignal.timeout(10000)})).catch(() => {});
    return progressWrites;
  }
  function mount() {
    visited.add(step);
    restore();
    for (const panel of document.querySelectorAll('[data-setup-step]')) panel.hidden = panel.dataset.setupStep !== step;
    if (['storage','proxy'].includes(step)) {
      $('setup' + step[0].toUpperCase() + step.slice(1) + 'Slot').append($('preferences'));
      for (const group of document.querySelectorAll('[data-setup-settings]')) group.hidden = group.dataset.setupSettings !== step;
    }

    if (step === 'finish' && !application) void checkLogs();
    setText('setupTitle', titles[steps.indexOf(step)]);
    renderControls(); update();
    document.dispatchEvent(new CustomEvent('setupstep', {detail:step}));
  }
  function validPage() {
    if (step === 'storage' && $('setupImportEnabled').checked && !$('setupImportSource').value.trim()) {
      folderFeedback('import','请选择要导入的文件夹，或取消勾选。');
      $('setupImportSelection').focus();return false;
    }
    if (step === 'storage' && $('setupCapacityLimited').checked && (!$('setupCapacityValue').checkValidity() || Number($('setupCapacityValue').value) <= 0)) {
      $('setupCapacityValue').reportValidity(); return false;
    }
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
    cancelFolder();
    advancing = true; renderControls();
    try {
      const candidate = plan();
      if (['storage','proxy'].includes(step)) await post('validate', {...candidate,page:step});
      if (!isOpen() || revision !== navigationRevision) return;
      if (draft) draft = candidate;
      setText('setupMessage','');
      step = next; void persist(); mount(); $('setupTitle').focus();
      if (step === 'welcome') void loadTerms();
    } catch (error) {
      if (isOpen() && revision === navigationRevision) setText('setupMessage', '本页尚未通过检查：' + error.message);
    } finally {advancing = false; renderControls();}
  }
  function defaults(page = step) {
    if (page === 'storage') return {storageDir:lastState?.defaultStorageDir || '',maxCacheGiB:'0',setupImportSource:''};
    if (page === 'proxy') return {upstreamMode:'direct',proxyBackend:'external-socks5',proxyImportID:'',proxyImportCount:'',proxyImportText:'',socks5Address:'',socks5Username:'',socks5Password:''};
    return null;
  }
  function atDefaults(page = step) {
    if (page === 'storage' && ($('setupCustomStorage').checked || $('setupCapacityLimited').checked || $('setupImportEnabled').checked)) return false;
    const values = defaults(page);
    return !values || Object.entries(values).every(([id,value]) => id === 'storageDir' ? pathKey($(id).value) === pathKey(value)
      : id === 'maxCacheGiB' ? $(id).value !== '' && Number($(id).value) === 0 : $(id).value === value);
  }
  function stepStatus(page) {
    // Presentation only: these markers never authorize navigation or consent.
    const changed = page === 'welcome' ? terms?.accepted === true
      : page === 'finish' ? !!lastState?.hosts.ready : !!draft && !atDefaults(page);
    return changed ? 'changed' : visited.has(page) ? 'default' : 'unvisited';
  }
  function renderNavigation() {
    $('setupCheckAccess').disabled = accessChecking || logsChecking || !connected;
    $('setupCheckAccess').dataset.busy = String(accessChecking || logsChecking);
    const locked = loading || !entered || advancing || applying || !!application || !!globalThis.swdProxyImportPending?.();
    stepButtons.forEach((button,index) => {
      button.disabled = locked || !canVisitStep(steps[index]);
      if (steps[index] === step) button.setAttribute('aria-current','step'); else button.removeAttribute('aria-current');
      const status = stepStatus(steps[index]);
      const label = {unvisited:'未浏览、未改动',default:'已浏览、默认值',changed:'有用户改动'}[status];
      button.dataset.status = status;
      button.setAttribute('aria-label',titles[index] + '：' + label);
      button.setAttribute('title',label);
    });
    $('setupBack').disabled = locked || step === 'welcome';
    $('setupBack').hidden = !!application;
    $('setupNext').disabled = loading || !entered || advancing || applying || checkingReceipt || waitingForRestart() || application?.phase === 'importing';
    $('setupNext').disabled ||= step === 'finish' && !connected && !restartProblem;
    $('setupNext').disabled ||= step === 'welcome' && !canVisitStep('storage');
    $('setupNext').disabled ||= !!globalThis.swdProxyImportPending?.();
    $('setupNext').hidden = false;
    $('setupNext').textContent = restartProblem ? '重新连接并核对' : application?.phase === 'uncertain' ? '核对应用结果' : application ? '返回控制台' : step === 'finish' ? '确认并应用' : '下一步';
    $('setupLeave').disabled = beforeConsentBusy || applying || waitingForRestart() || application?.phase === 'importing';
    $('setupLeave').textContent = window.swdTermsGate ? '退出' : application ? '返回控制台' : '放弃更改并退出引导';
    $('setupReset').hidden = !defaults() || !!application;
    $('setupReset').disabled = locked || atDefaults();
    setText('setupTermsLabel','阅读并同意');
    setText('setupAcceptTerms',terms?.accepted === true ? '已确认条款' : terms ? '同意完整条款' : '重新读取条款');
    statusIcon('setupTermsIcon',terms?.accepted === true ? 'ok' : 'unknown');
  }
  function renderSummary(s) {
    const p = {...(draft || plan())}, current = s.activeSettings || s.settings;
    if (application?.importSource) p.importSource = application.importSource;
    const mode = value => ({direct:'直连',proxy:'强制代理',socks5:'强制代理',auto:'直连 + 代理'}[value] || '未选择');
    const capacity = bytes => bytes ? (bytes / 1073741824) + ' GiB' : '不限';
    const change = (label,before,after) => label + '：' + (before === after ? after : before + ' → ' + after);
    const storage = path => pathKey(path) === pathKey(s.defaultStorageDir) ? '默认' : '自定义 · ' + path;
    const lines = [change('存储',storage(current.storageDir),storage(p.storageDir)),change('缓存空间上限',capacity(current.maxCacheBytes),capacity(p.maxCacheBytes))];
    lines.push(p.importSource ? '导入：' + p.importSource + '\n目标：' + p.storageDir + ' · 保留源文件' : '导入：不执行');
    lines.push(change('网络连接',mode(current.upstreamMode),mode(p.upstreamMode)));
    if (p.upstreamMode !== 'direct') lines.push('代理后端：' + (p.proxyBackend === 'mihomo' ? '内嵌 Mihomo' : '外部 SOCKS5'));
    if (p.proxyImportID) lines.push('节点库：将替换为本次导入的 ' + $('proxyImportCount').value + ' 个节点（保存后重启生效）');
    if (p.upstreamMode !== 'direct' && p.proxyBackend !== 'mihomo') {
      lines.push(change('代理地址',current.socks5Address || '未设置',p.socks5Address || '未设置'));
      lines.push(change('认证用户',current.socks5Username || '无',p.socks5Username || '无'));
      if (p.socks5Password) lines.push('代理密码：将更新');
      else if (p.socks5Password === '' && s.socks5PasswordSet) lines.push('代理密码：将清除');
    }
    // Previously saved advanced changes are also activated by the restart.
    let advancedChanged = false;
    for (const [key,label] of [['autoStartCDN','自动启动播放网关'],['autoStartQueue','自动启动队列'],['queuePrefetchCount','队列预缓存数'],['requestRetentionDays','请求保留天数'],['throughputIntervalMinutes','测速间隔（分钟）'],['downloadUpstream','回源线路'],['manualLogDir','手动日志位置'],['logDir','日志目录']]) {
      if (current[key] !== s.settings[key]) {
        advancedChanged = true;
        lines.push(change(label,String(current[key]),String(s.settings[key])) + '（此前已保存）');
      }
    }
    setText('setupSummary',lines.join('\n'));
    const changed = advancedChanged || !!p.proxyImportID || ['storageDir','maxCacheBytes','upstreamMode','proxyBackend','socks5Address','socks5Username'].some(key =>
      key === 'storageDir' ? pathKey(current[key]) !== pathKey(p[key]) : key === 'proxyBackend' ? (current[key] || 'external-socks5') !== p[key] : (current[key] ?? '') !== (p[key] ?? '')) || (p.socks5Password === '' && s.socks5PasswordSet);
    const restart = changed ? '将重启程序，正在进行的播放和任务会中断。' : p.socks5Password || s.restartRequired ? '如最终设置与当前运行配置不同，将重启程序并中断播放和任务。' : '';
    setText('setupApplyHint',restart + (p.importSource ? (changed ? '重启后开始导入。' : '确认后开始导入。') + '关闭网页不影响导入，重启程序会中止导入。' : ''));
    setText('setupApplyResult',application ? (application.configSaved ? '配置已保存。' : '') + (restartProblem || (application.phase === 'restart' ? '正在重启，之后执行导入…' : application.phase === 'completed' ? (application.importStarted ? '设置已应用，导入已启动；进度可在设置页查看。' : '清单已应用。') : application.error || '正在应用…')) : '');
    $('setupApplyHint').hidden = !!application;
  }
  const networkNames = {catalog:'歌曲列表',playback_url:'播放地址',resource:'视频下载'};
  const networkStatusIDs = {catalog:'setupCatalogStatus',playback_url:'setupPlaybackStatus',resource:'setupResourceStatus'};
  function statusIcon(id, state) {
    setText(id,state === 'ok' ? '✓' : state === 'failed' ? '!' : '○');
    $(id).dataset.state = state;
  }
  const freshNetworkResult = r => !r.validUntil || r.validUntil.startsWith('0001') || Date.parse(r.validUntil) > Date.now();
  function networkSample(r, minutes) {
    const saved = r.lastThroughput;
    const speed = saved ? saved.bytes / (saved.durationNs / 1e9) : r.estimatedSpeedBPS;
    const at = saved?.observedAt || r.throughputObservedAt;
    return {speed:Number.isFinite(speed) && speed > 0 ? speed : null, fresh:Date.now() < Date.parse(at) + minutes * 60000};
  }
  function renderNetworkOverview(diagnostic, health, minutes) {
    const results = health.results || [];
    const checks = Object.entries(networkNames).map(([operation,label]) => {
      const group = results.filter(r => r.operation === operation);
      const available = group.filter(r => r.state === 'available' && freshNetworkResult(r));
      const failed = group.length > 0 && group.every(r => r.state === 'unavailable' && freshNetworkResult(r));
      let text = available.length ? '检查成功' + (available.length < group.length ? '，部分线路未通过或待确认' : '') : failed ? '检查失败' : '尚未确认';
      const latencies = available.map(r => r.estimatedLatencyMS).filter(ms => Number.isFinite(ms) && ms >= 0);
      if (latencies.length) text += ' · 响应 ' + Math.min(...latencies).toFixed(0) + ' ms';
      if (operation === 'resource') {
        const speeds = available.map(r => networkSample(r,minutes)).filter(s => s.fresh && s.speed !== null).map(s => s.speed);
        text += speeds.length ? ' · 最高 ' + (Math.max(...speeds) / 1048576).toFixed(2) + ' MiB/s' : ' · 暂无有效测速';
      }
      setText(networkStatusIDs[operation],text);
      statusIcon(networkStatusIDs[operation].replace('Status','Icon'),available.length ? 'ok' : failed ? 'failed' : 'unknown');
      return {available:available.length > 0,failed,text:label + '：' + text};
    });
    const mode = $('upstreamMode').value, proxy = mode && mode !== 'direct';
    const proxyValid = $('proxyBackend').value === 'mihomo' ? !!$('proxyImportID').value || !!lastState?.proxyNodes?.saved && !lastState?.proxyNodes?.error : $('socks5Address').value.trim() && $('socks5Address').checkValidity();
    setText('setupDirectAdvice',!mode ? '请在下方选择直连或代理，再继续。' : proxy ? (proxyValid ? '以上仅为直连检测结果，尚未验证代理。' : '请先在下方填写有效的代理地址，或导入受支持的节点，再继续。') : diagnostic.running ? '正在检测直连，可继续下一步。' : diagnostic.error ? '检测未完成。建议重新检测，也可先继续设置。' : checks.every(c => c.available) ? '直连检查通过，建议保持直连并继续。' : checks.some(c => c.failed) ? '部分直连检查失败。建议重试；需要代理时可在下方配置。' : checks.some(c => c.available) ? '部分检查已通过，其余尚未确认。建议先保持直连并继续；若游戏加载失败，再配置代理。' : '尚未取得检测结果。可以重试检测，也可先继续设置。');
    $('setupDirectAdvice').hidden = !!mode && (!proxy || !!proxyValid) && !diagnostic.error && !checks.some(c => c.failed);
  }
  function update() {
    if (isOpen() && !entered && window.location.hash === '#/setup') begin();
    setText('setupTermsState',terms ? terms.accepted ? '已保存。' : '点击同意后立即保存。' : '尚未读到确认记录');
    const s = lastState;
    if (!s) {
      setText('setupSaveState','');
      renderNavigation(); return;
    }
    if (startingSession && !window.swdTermsGate && connected) {
      startingSession = false;
      if (entered) initializeDraft();
      setText('setupMessage','');
      if (entered && application) {step = 'finish'; mount(); return;}
    }
    if (applicationID && s.setupApplication?.id === applicationID) application = s.setupApplication;
    // A durable success receipt completes setup. Access/service controls live
    // in the console; recovery must never repeat application or launch them.
    if (entered && isOpen() && application?.phase === 'completed' && !applying && !checkingReceipt && !startingSession) {
      exit('#/home'); return;
    }
    if (application?.phase === 'restart' && !reconnecting && !restartProblem) void recoverRestart();
    renderNavigation();
    if (isOpen() && step === 'storage' && checkedPath !== $('storageDir').value.trim()) void checkStorage();
    const diagnostic = s.setupNetwork || {}, health = diagnostic.monitor || {};
    const status = diagnostic.running ? '正在检测…' : diagnostic.error ? '未完成：' + diagnostic.error : '';
    setText('setupDirectStatus',networkStartError || (diagnostic.error ? status : ''));
    setText('setupNetworkBackground',status);
    for (const id of ['setupRetryNetwork']) {
      $(id).disabled = networkStarting || !!diagnostic.running || !connected;
      $(id).dataset.busy = String(networkStarting || !!diagnostic.running);
      $(id).setAttribute('aria-busy',String(networkStarting || !!diagnostic.running));
      $(id).setAttribute('title',networkStarting || diagnostic.running ? '正在检测直连网络…' : '重新检测直连网络');
    }
    const minutes = (health.policy?.throughputInterval || 20 * 60 * 1e9) / (60 * 1e9);
    renderNetworkOverview(diagnostic,health,minutes);
    const results = health.results || [];
    renderStableList('setupDirectResults',results,r => r.operation + '/' + r.route + '/' + r.channelID,r => {
      const row = renderHealthResult(r,minutes,diagnostic.running ? '正在测量' : '等待重试');
      row.prepend(recentElement('p','',({catalog:'清单',playback_url:'播放地址',resource:'视频资源'}[r.operation] || r.operation) + ' · ' + r.route)); return row;
    },results.map(r => [!freshNetworkResult(r),!networkSample(r,minutes).fresh]));
    setText('setupSaveState',application ? '' : s.setupSettingsError || (!connected ? '连接中断，草稿仍保留；重连后可继续。' : ''));
    $('setupStoragePath').value = $('storageDir').value;
    $('setupStorageMoveHint').hidden = !s.settings.storageDir || pathKey($('storageDir').value) === pathKey(s.settings.storageDir);
    $('setupImportSelection').value = $('setupImportSource').value;
    for (const id of ['setupImportFolder','setupImportDetails']) $(id).hidden = !$('setupImportEnabled').checked;
    $('setupProxySavedHint').hidden = !s.socks5PasswordSet;
    $('setupProxyNewHint').hidden = !!s.socks5PasswordSet;
    setText('setupHosts','hosts 状态：' + (s.hosts.ready ? '已接入' : s.hosts.conflict ? (s.hosts.message || '存在冲突，请处理后重试') : '未接入'));
    statusIcon('setupHostsIcon',s.hosts.ready ? 'ok' : s.hosts.conflict ? 'failed' : 'unknown');
    renderHostsControls();
    const portText = (ok,owner) => ok ? 'ready' : owner ? '被 ' + owner.name + '（PID ' + owner.pid + '）占用' : '被占用或不可用';
    setText('setupPort80','80 (HTTP) 端口状态：' + portText(s.portOK,s.portOwner));
    setText('setupPort443','443 (HTTPS) 端口状态：' + portText(s.httpsPortOK,s.httpsPortOwner));
    statusIcon('setupPort80Icon',s.portOK ? 'ok' : 'failed');
    statusIcon('setupPort443Icon',s.httpsPortOK ? 'ok' : 'failed');
    statusIcon('setupLogsIcon',logsChecking ? 'unknown' : logsState);
    $('setupLogsStatus').hidden = logsState === 'ok' && !logsChecking;
    const portsOK = s.portOK && s.httpsPortOK;

    setText('setupAccessCheck',!portsOK ? '接入检查未通过：端口被占用，请关闭占用程序后重试。' : logsChecking || !logsState ? '正在检查接入条件…' : logsState === 'ok' ? '接入检查通过' : '视频接入条件已就绪；日志尚未确认，不影响视频缓存。');
    $('setupAccessCheck').hidden = portsOK && logsState === 'ok' && !logsChecking;
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
  async function reconcileApplication() {
    if (checkingReceipt || !applicationID || !entered) return;
    const id = applicationID, request = ++receiptRequest;
    checkingReceipt = true; renderControls();
    try {
      const response = await fetch('/api/setup/receipt', {method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':token},body:JSON.stringify({id}),signal:AbortSignal.timeout(15000)});
      const result = await response.json();
      if (!entered || request !== receiptRequest || applicationID !== id) return;
      if (response.status === 404) application = {id,phase:'missing',error:result.error};
      else {
        if (!response.ok || result.id !== id) throw new Error(result.error || '回执 ID 不匹配');
        application = result;
      }
    } catch (error) {
      if (entered && request === receiptRequest && applicationID === id) application = {id,phase:'uncertain',error:'原应用回执尚未确认：' + error.message + '。请重新核对；不会重复应用或导入。'};
    } finally {
      if (request === receiptRequest) {checkingReceipt = false; renderControls(); update();}
    }
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
      if (application?.phase === 'uncertain') await reconcileApplication();
      // Keep the same id for retries after an uncertain response.
    } finally {applying = false; renderControls(); update();}
  }
  function renderHostsControls() {
    const ready = !!lastState?.hosts.ready;
    $('setupHostsEnable').textContent = '接入';
    $('setupHostsEnable').hidden = ready;
    $('setupHostsEnable').disabled = hostsUpdating || beforeConsentBusy || !connected || !lastState || ready;
    $('setupHostsRestore').hidden = !ready && !lastState?.hosts.conflict && !lastState?.hosts.needsMigration;
    $('setupHostsRestore').disabled = hostsUpdating || beforeConsentBusy || !connected || !lastState;
  }
  async function changeHosts(desired) {
    if (hostsUpdating || beforeConsentBusy || !connected || !lastState || (desired && lastState.hosts.ready)) return;
    hostsUpdating = true; update();
    setText('setupHostsResult','正在' + (desired ? '启用游戏接入' : '恢复游戏直连') + '，请确认管理员权限提示。');
    try {
      const response = await fetch('/api/hosts/' + (desired ? 'enable' : 'disable'),{method:'POST',headers:{'X-SWD-Token':token,'Content-Type':'application/json'},body:'{}',signal:AbortSignal.timeout(120000)});
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || '操作失败');
      setText('setupHostsResult','');
    } catch (error) {
      const message = '操作未确认：' + error.message + '。请核对实际状态后重试。';
      setText('setupHostsResult',message);
    }
    finally {await requestRefresh(); hostsUpdating = false; update();}
  }
  $('setupHostsEnable').onclick = () => changeHosts(true);
  $('setupHostsRestore').onclick = () => changeHosts(false);
  const exit = (destination, explicitDiscard = false) => {
    if (window.swdTermsGate) return beforeConsentAction('exit','程序正在退出，可以关闭页面。');
    if (applying || waitingForRestart() || (!explicitDiscard && !confirmDiscard())) return;
    void persist(application?.phase === 'completed' ? 'completed' : 'skipped'); discard();
    if (destination.startsWith('#')) window.location.hash = destination; else window.location.assign(destination);
  };
  stepButtons.forEach((button,index) => {button.onclick = () => {
    if (steps[index] !== step) return go(steps[index]);
    if (step === 'welcome') {navigationRevision++; return loadTerms();}
  };});
  $('setupBack').onclick = () => go(steps[Math.max(0,steps.indexOf(step) - 1)]);
  $('setupNext').onclick = () => {
    if (restartProblem) return recoverRestart();
    if (application?.phase === 'uncertain') return reconcileApplication();
    return application ? exit('#/home') : step === 'finish' ? apply() : go(steps[steps.indexOf(step) + 1]);
  };
  $('setupLeave').onclick = () => exit('#/home',true);
  window.swdSetupSubmit = () => {if (!isOpen()) return false; void $('setupNext').onclick(); return true;};
  $('setupReset').onclick = () => {
    if ($('setupReset').disabled) return;
    if (step === 'proxy') globalThis.swdResetProxyImport?.();
    for (const [id,value] of Object.entries(defaults() || {})) $(id).value = value;
    cancelFolder();
    if (step === 'storage') {$('setupCustomStorage').checked = false;$('setupImportEnabled').checked = false;syncCapacity();}
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
    if (window.swdTermsGate && to !== 'setup') return false;
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
  $('setupImportSource').addEventListener('input',update);
  window.addEventListener('pagehide',cancelFolder);
  window.addEventListener('beforeunload',event => {
    if (!exiting && hasDraftChanges()) {event.preventDefault(); event.returnValue = '';}
  });
  async function beforeConsentAction(path, message) {
    if (beforeConsentBusy || hostsUpdating) return;
    beforeConsentBusy = true; renderControls();
    const feedback = path === 'hosts/disable' ? 'setupRestoreResult' : 'setupMessage';
    if (path === 'hosts/disable') setText(feedback,'正在恢复游戏直连，请确认管理员权限提示。');
    try {
      const response = await fetch('/api/' + path,{method:'POST',headers:{'Content-Type':'application/json','X-SWD-Token':token},body:'{}',signal:AbortSignal.timeout(120000)});
      if (!response.ok) throw new Error(await response.text());
      setText(feedback,message);
    } catch (error) {setText(feedback,error.message);}
    finally {if (path === 'hosts/disable' && !window.swdTermsGate) await requestRefresh();beforeConsentBusy = false; renderControls();}
  }
  $('setupRestoreHelp').onclick = () => beforeConsentAction('hosts/disable','已恢复游戏直连。');
  if (firstEntry) {
    function presence(action) {
      if (!window.swdTermsGate) return;
      fetch('/api/terms/' + action,{method:'POST',keepalive:true,headers:{'Content-Type':'application/json','X-SWD-Token':token},body:JSON.stringify({pageID:firstEntry.pageID})}).catch(()=>{});
    }
    window.addEventListener('pagehide',()=>presence('leave'));
    window.addEventListener('pageshow',event=>{if(event.persisted)presence('return');});
  }
  (async () => {
    try {
      if (firstEntry) {step = 'welcome'; return;}
      const response = await fetch('/api/setup',{signal:AbortSignal.timeout(10000)});
      if (!response.ok) throw new Error('无法读取上次引导进度；可以重新开始。');
      const progress = await response.json();
      if (steps.includes(currentStep(progress.step)) && progress.state === 'active') step = currentStep(progress.step);
      if (!window.swdInitialRoute && window.location.hash === '#/home' && progress.state === 'active') window.location.hash = '#/setup';
    } catch (error) {setText('setupMessage',error.message);}
    finally {loading = false; if (isOpen()) begin(); else update();}
  })();
})();
