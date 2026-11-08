import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Button, Link, MessageBar, MessageBarBody, MessageBarTitle, Spinner, Title2, Tooltip, makeStyles, shorthands, tokens } from '@fluentui/react-components';
import { ConsoleContext, useConsole } from './hooks';
import { Brand, Notification, RecordDetails, StatusBadge, NavigationLayout, pageLinks, isDocumentPage, useThemePreference } from './ui';
import { AboutPage } from './documents';
import { Home } from './home-library';
import { SettingsAndNetwork } from './settings-page';
import { RecoverySettings } from './recovery-settings';
import { Library } from './library';
import { TermsConsent } from './terms-consent';
import { RequestTimeline } from './request-timeline';
import { bootstrap } from './bootstrap';
import { request } from './api';
import { exitConnectionMessage } from './types';
import { HostsAuthorizationNotice, VideoImportSuggestion } from './hosts-notice';
import { VRCXProvider } from './vrcx';

const useStyles = makeStyles({
  exitButton: {
    '@media (forced-colors: none)': {
      '&:enabled:hover': {
        backgroundColor: tokens.colorStatusDangerBackground3Hover,
        color: tokens.colorNeutralForegroundOnBrand,
        '&:not(:focus-visible)': shorthands.borderColor(tokens.colorStatusDangerBackground3Hover),
      },
      '&:enabled:active': {
        backgroundColor: tokens.colorStatusDangerBackground3Pressed,
        color: tokens.colorNeutralForegroundOnBrand,
        '&:not(:focus-visible)': shorthands.borderColor(tokens.colorStatusDangerBackground3Pressed),
      },
    },
  },
});

const pages = {home:'加速',timeline:'历史',library:'曲库',network:'网络',settings:'设置',about:'关于'};
type Page = keyof typeof pages;
const anchors:Record<string,Page>={activation:'home',service:'home',queue:'home',overview:'library',downloads:'home','download-settings':'settings',recent:'timeline',network:'network','network-proxy':'settings','proxy-settings':'settings','network-access':'home','network-tests':'network',batch:'library',preferences:'settings',restoreHostsOnStopOrExit:'settings',prepareAheadSeconds:'settings',videoBudgetGiB:'settings','storage-settings':'settings','video-import':'library','vrcx-import':'library',help:'about',terms:'about',licenses:'about',logs:'about','help-start':'about','help-playback':'about','help-storage':'about','help-stop':'about','help-paths':'about'};
export function route(hash: string): Page {
  const key = hash.replace(/^#\/?/,'').split('?')[0];
  return Object.hasOwn(pages,key) ? key as Page : anchors[key] || 'home';
}
export function App() {
  const styles = useStyles();
  const [accepted,setAccepted]=useState(!bootstrap.termsGate);
  const [anchor,setAnchor]=useState(location.hash.slice(1)||location.pathname.slice(1));
  const [page, setPage] = useState<Page>(() => route(location.hash&&location.hash!=='#main'?location.hash:'#'+location.pathname));
  const documentPage=isDocumentPage(page);
  const model = useConsole(accepted&&!bootstrap.recovery, page), main = useRef<HTMLElement>(null), initial = useRef(true);
  const tabAnchor=useRef<string|null>(null);
  const selectTab=(next:string)=>{
    if(location.hash==='#'+next)return;
    // Keep browser history without native fragment navigation moving tab focus.
    history.pushState(null,'','#'+next);tabAnchor.current=next;setAnchor(next);
  };
  useEffect(() => {
    const originalHash=location.hash;
    if(isDocumentPage(location.pathname.slice(1)))history.replaceState(null,'','/'+location.search+(originalHash&&originalHash!=='#main'?originalHash:'#/'+location.pathname.slice(1)));
    const change = () => {
      if(location.hash==='#main')return;
      setPage(route(location.hash));setAnchor(location.hash.slice(1));
    };
    addEventListener('hashchange',change); return () => removeEventListener('hashchange',change);
  }, []);
  useEffect(()=>{
    if(accepted||!bootstrap.termsGate)return;
    const presence=(path:string)=>{void fetch('/api/terms/'+path,{method:'POST',keepalive:true,headers:{'Content-Type':'application/json','X-SWD-Token':bootstrap.token},body:JSON.stringify({pageID:bootstrap.termsGate!.pageID})}).catch(()=>{});};
    const leave=()=>presence('leave'), back=(e:PageTransitionEvent)=>{if(e.persisted)presence('return');};
    addEventListener('pagehide',leave);addEventListener('pageshow',back);return()=>{removeEventListener('pagehide',leave);removeEventListener('pageshow',back);};
  },[accepted]);
  useLayoutEffect(() => {
    document.title = pages[page]+' · Still Wanna Dance';
    // Tab activation updates the URL while Fluent keeps focus in the tab list.
    if(tabAnchor.current===anchor)return;
    if (!initial.current) main.current?.focus({preventScroll:true});
    initial.current = false;
    if (!Object.hasOwn(anchors,anchor)) window.scrollTo({top:0,left:0,behavior:'instant'});
  }, [page,anchor,accepted]);
  const s = model.status.data;
  const [updateNotice,setUpdateNotice]=useState('');
  const updateNoticeClaimed=useRef(false);
  useEffect(()=>{
    if(!accepted||model.stopped||!s?.update?.startupNotice||updateNoticeClaimed.current)return;
    updateNoticeClaimed.current=true;
    void request<{version?:string}>('update/notice',{}).then(result=>{
      if(result.version)setUpdateNotice(result.version);
      model.refresh();
    }).catch(()=>{updateNoticeClaimed.current=false;});
  },[accepted,model.stopped,s?.update]);
  const [proxyNotice,setProxyNotice]=useState(false);
  useEffect(()=>{
    if(!accepted||page!=='home'||!s?.proxyModeMigrated)return;
    try {
      if(sessionStorage.getItem('swd-proxy-mode-migrated')===bootstrap.token)return;
      sessionStorage.setItem('swd-proxy-mode-migrated',bootstrap.token);
    }catch{ /* The mounted notification also deduplicates status updates. */ }
    setProxyNotice(true);
  },[accepted,page,s?.proxyModeMigrated]);
  const {setPreference}=useThemePreference();
  // Reconcile every snapshot, even if a delayed save changed only local appearance.
  useEffect(()=>{if(s)setPreference(s.activeSettings.theme);},[s,setPreference]);
  useLayoutEffect(()=>{
    const fromTab=tabAnchor.current===anchor;tabAnchor.current=null;
    if(fromTab)return;
    if((s || documentPage) && Object.hasOwn(anchors,anchor)){
      const target=document.getElementById(anchor==='downloads'||anchor==='queue'?'block-demands':anchor==='service'||anchor==='network-access'?'activation':anchor);target?.focus();target?.scrollIntoView();
    }
  },[page,!!s,anchor,documentPage]);
  const fault=bootstrap.recovery||s?.storageError||s?.workError||model.status.error;
  const statusText=model.stopped?'未连接':bootstrap.recovery?'启动失败':model.status.error?s?'更新失败':'读取失败':s?.storageError?'存储故障':s?.workError?'存储故障':s?.storageConfirmation?'待确认存储':s?'已连接':'连接中';
  const runtimeBadge=<StatusBadge label="后台程序" color={model.stopped?'informative':fault?'danger':s?.storageConfirmation?'warning':s?'success':'brand'}>{statusText}</StatusBadge>;
  const runtimeStatus=<div className="runtime-status">{(fault||s?.storageConfirmation)&&!bootstrap.recovery?<RecordDetails title="后台状态" openOnHover trigger={<button type="button" className="status-badge-button" aria-label={`后台程序：${statusText}，查看详情`}>{runtimeBadge}</button>}>{s?.storageConfirmation&&<><p className="wrap">{s.storageConfirmation}</p><Link href="#storage-settings">确认存储设置</Link></>}{model.status.error&&<><p>无法读取程序状态，请检查 SWD 是否仍在运行。</p>{s&&<p>当前显示上次状态。</p>}</>}{s?.storageError&&<><p>本地存储不可用，新的下载已停止。</p><p>检查所选文件、磁盘空间和目录权限，修复后重新打开应用。</p><p className="wrap">首个错误：{s.storageError}</p></>}{s?.workError&&<><p>临时视频缓存目录不可用，视频服务和下载无法启动。检查磁盘和目录权限，修复后退出并重新打开应用；也可在下载与缓存设置中选择其他目录，保存后重启。</p><p className="wrap">原因：{s.workError}</p></>}</RecordDetails>:runtimeBadge}</div>;
  const feedback=<div className="app-feedback">
    <Notification message={model.message} error={model.messageError} persistent={model.busy}/>
    <Notification message={updateNotice?`发现新版本 ${updateNotice}`:undefined} action={{href:'#/about',label:'查看更新'}}/>
    {proxyNotice&&<Notification message="上游连接已改为代理" body="自动选择模式已移除，原代理配置已保留。可在设置中切换为直连。" action={{href:'#network-proxy',label:'网络设置'}}/>}
    <HostsAuthorizationNotice authorizationID={s?.hosts.authorizationID}/>
    {accepted&&!bootstrap.recovery&&<VideoImportSuggestion/>}
    {!s&&accepted&&!bootstrap.recovery&&!documentPage&&!model.status.error&&<Spinner label="正在读取程序状态…"/>}
  </div>;
  return <ConsoleContext.Provider value={model}><div className="shell">
    <Link className="skip-link" href="#main" onClick={e => { e.preventDefault(); main.current?.focus(); }}>跳到主要内容</Link>
    <header><Brand/><div className="runtime-controls">{accepted&&runtimeStatus}<Tooltip content={'所有服务和后台任务将停止。'+exitConnectionMessage(s)} relationship="description" positioning="below-end"><Button className={styles.exitButton} disabled={bootstrap.recovery?model.busy||model.stopped:model.unavailable} onClick={()=>void model.action('exit')}>退出程序</Button></Tooltip></div></header>
    <NavigationLayout items={pageLinks()} selected={page}>
    <main ref={main} id="main" tabIndex={-1}><div className="page-heading"><Title2 as="h1">{pages[page]}</Title2></div>
      {feedback}
      {bootstrap.recovery&&!model.stopped&&<MessageBar intent="error"><MessageBarBody><MessageBarTitle>SWD 启动失败</MessageBarTitle><p className="wrap">{bootstrap.recovery.reason}</p><p>可前往<Link href="#/settings">修改设置</Link>。如果配置无法读取，请根据原因修复文件或运行环境，再重新启动 SWD。</p></MessageBarBody></MessageBar>}
      {bootstrap.recovery&&<RecoverySettings visible={page==='settings'} anchor={anchor} onTabSelect={selectTab}/>}
      {s && <VRCXProvider target={s.activeSettings.databasePath} page={page}><div hidden={page !== 'home'}><Home visible={page==='home'}/></div><div hidden={page !== 'timeline'}><RequestTimeline visible={page==='timeline'}/></div><div hidden={page!=='library'}><Library visible={page==='library'} anchor={anchor}/></div><SettingsAndNetwork settingsVisible={page==='settings'} networkVisible={page==='network'} anchor={anchor} onTabSelect={selectTab}/>
      </VRCXProvider>}
      <div hidden={page!=='about'}><AboutPage visible={accepted&&page==='about'} anchor={anchor} onTabSelect={selectTab} paths={s?{...bootstrap.paths,Database:s.activeSettings.databasePath,Videos:s.activeSettings.videoDir}:bootstrap.paths||{}} update={s?.update} refreshUpdate={model.refresh} updateUnavailable={!accepted||!s||!!model.status.error||model.stopped}/></div>
      </main>
    </NavigationLayout>{model.dialog}{!accepted&&<TermsConsent onAccepted={()=>setAccepted(true)}/> }
  </div></ConsoleContext.Provider>;
}
