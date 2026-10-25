import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Button, Link, Spinner, Title2, Tooltip } from '@fluentui/react-components';
import { ConsoleContext, useConsole } from './hooks';
import { Brand, Notification, RecordDetails, StatusBadge, NavigationLayout, pageLinks, isDocumentPage, DocumentFooter, useThemePreference } from './ui';
import { AboutPage } from './documents';
import { Home } from './home-library';
import { SettingsAndNetwork } from './settings-page';
import { Library } from './library';
import { TermsConsent } from './terms-consent';
import { RequestTimeline } from './request-timeline';
import { bootstrap } from './bootstrap';
import { exitConnectionMessage } from './types';

const pages = {home:'加速',timeline:'历史',library:'曲库',network:'网络',settings:'设置',about:'关于'};
type Page = keyof typeof pages;
const anchors:Record<string,Page>={activation:'home',service:'network',queue:'home',overview:'library',cache:'library',downloads:'home','download-settings':'settings',recent:'timeline',network:'network','network-proxy':'settings','proxy-settings':'settings','network-access':'network','network-tests':'network',batch:'library',preferences:'settings',prepareAheadSeconds:'settings',maxCacheGiB:'settings','storage-settings':'settings','video-import':'library','song-preferences':'library',help:'about',terms:'about',licenses:'about','help-start':'about','help-playback':'about','help-storage':'about','help-stop':'about','help-paths':'about'};
export function route(hash: string): Page {
  if (hash === '#/upstream' || hash === '#/monitor') return 'network';
  const key = hash.replace(/^#\/?/,'').split('?')[0];
  return Object.hasOwn(pages,key) ? key as Page : anchors[key] || 'home';
}
export function App() {
  const [accepted,setAccepted]=useState(!bootstrap.termsGate);
  const [anchor,setAnchor]=useState(location.hash.slice(1)||location.pathname.slice(1));
  const [page, setPage] = useState<Page>(() => route(location.hash&&location.hash!=='#main'?location.hash:'#'+location.pathname));
  const documentPage=isDocumentPage(page);
  const model = useConsole(accepted, page), main = useRef<HTMLElement>(null), initial = useRef(true);
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
    if (!initial.current) main.current?.focus({preventScroll:true});
    initial.current = false;
    if (!Object.hasOwn(anchors,anchor)) window.scrollTo({top:0,left:0,behavior:'instant'});
  }, [page,anchor,accepted]);
  const s = model.status.data;
  const {setPreference}=useThemePreference();
  // Reconcile every snapshot, even if a delayed save changed only local appearance.
  useEffect(()=>{if(s)setPreference(s.activeSettings.theme);},[s,setPreference]);
  useLayoutEffect(()=>{
    if((s || documentPage) && Object.hasOwn(anchors,anchor)){
      const target=document.getElementById(anchor);target?.focus();target?.scrollIntoView();
    }
  },[page,!!s,anchor,documentPage]);
  const fault=s?.storageError||model.status.error;
  const statusText=model.stopped?'未连接':model.status.error?s?'更新失败':'读取失败':s?.storageError?'存储故障':s?'已连接':'连接中';
  const runtimeStatus=<div className="runtime-status"><StatusBadge label="后台程序" color={model.stopped?'informative':fault?'danger':s?'success':'brand'}>{statusText}</StatusBadge>{fault&&<RecordDetails title="后台状态">{model.status.error&&<><p>无法读取程序状态，请检查 SWD 是否仍在运行。</p>{s&&<p>当前显示上次状态。</p>}</>}{s?.storageError&&<><p>本地存储故障，已停止新的下载。已保存的视频仍可读取。</p><p>检查磁盘空间和目录权限，修复后退出并重新打开整个应用。</p><p className="wrap">首个错误：{s.storageError}</p></>}</RecordDetails>}</div>;
  const feedback=<div className="app-feedback">
    <Notification message={model.message} error={model.messageError} persistent={model.busy}/>
    {!s&&accepted&&!documentPage&&!model.status.error&&<Spinner label="正在读取程序状态…"/>}
  </div>;
  return <ConsoleContext.Provider value={model}><div className="shell">
    <Link className="skip-link" href="#main" onClick={e => { e.preventDefault(); main.current?.focus(); }}>跳到主要内容</Link>
    <header><Brand/><div className="runtime-controls">{accepted&&runtimeStatus}<Tooltip content={'所有服务和后台任务将停止。'+exitConnectionMessage(s)} relationship="description" positioning="below-end"><Button disabled={model.unavailable} onClick={()=>void model.action('exit')}>退出程序</Button></Tooltip></div></header>
    <NavigationLayout items={pageLinks()} selected={page}>
    <main ref={main} id="main" tabIndex={-1}><div className="page-heading"><Title2 as="h1">{pages[page]}</Title2></div>
      {feedback}
      {s && <><div hidden={page !== 'home'}><Home visible={page==='home'}/></div><div hidden={page !== 'timeline'}><RequestTimeline visible={page==='timeline'}/></div><div hidden={page!=='library'}><Library visible={page==='library'} anchor={anchor}/></div><SettingsAndNetwork settingsVisible={page==='settings'} networkVisible={page==='network'} anchor={anchor}/>
      </>}
      {page==='about'&&<AboutPage anchor={anchor} paths={s?{...bootstrap.paths,Storage:s.activeSettings.storageDir}:bootstrap.paths||{}}/>}
      </main><footer hidden={documentPage}><span>关闭网页后 SWD 仍在后台运行。</span><Link href="#/help">使用帮助</Link><Link href="#preferences">退出设置</Link></footer>
    <DocumentFooter page={page==='about'&&anchor.includes('help')?'help':page}/>
    </NavigationLayout>{model.dialog}{!accepted&&<TermsConsent onAccepted={()=>setAccepted(true)}/> }
  </div></ConsoleContext.Provider>;
}
