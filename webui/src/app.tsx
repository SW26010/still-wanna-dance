import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Badge, Button, Link, Spinner, Title2 } from '@fluentui/react-components';
import { ConsoleContext, useConsole } from './hooks';
import { Brand, Feedback, NavigationLayout, pageLinks, isDocumentPage, DocumentFooter } from './ui';
import { AboutPage } from './documents';
import { Home } from './home-library';
import { Settings } from './settings-page';
import { Library } from './library';
import { Setup } from './setup';
import { RequestTimeline } from './request-timeline';
import { bootstrap } from './bootstrap';
import { accelerationLabel, exitConnectionMessage } from './types';

const pages = {home:'视频加速',timeline:'时间线',library:'曲库',settings:'设置',setup:'使用引导',about:'关于'};
type Page = keyof typeof pages;
const descriptions:Partial<Record<Page,string>>={timeline:'查看历史请求与最近 72 小时的首正文等待。',home:'为 VRChat WannaDance 复用视频，并提前准备房间队列。',library:'查找歌曲与共享视频，管理本地文件和下载。',settings:'调整常规、存储、下载与网络配置。',setup:'设置、导入、检测和接入；可随时返回或离开。'};
const anchors:Record<string,Page>={activation:'home',service:'settings',queue:'home',overview:'library',cache:'library',downloads:'home','download-settings':'settings',recent:'timeline',network:'settings','network-proxy':'settings','network-access':'settings',batch:'library',preferences:'settings',prepareAheadSeconds:'settings',maxCacheGiB:'settings','storage-settings':'settings','video-import':'library','song-preferences':'library',help:'about',terms:'about',licenses:'about','help-start':'about','help-playback':'about','help-storage':'about','help-stop':'about','help-paths':'about'};
export function route(hash: string): Page {
  if (hash === '#/upstream' || hash === '#/monitor') return 'settings';
  const key = hash.replace(/^#\/?/,'').split('?')[0];
  return Object.hasOwn(pages,key) ? key as Page : anchors[key] || 'home';
}
export function App() {
  const [accepted,setAccepted]=useState(!bootstrap.termsGate);
  const [anchor,setAnchor]=useState(location.hash.slice(1)||location.pathname.slice(1));
  const [page, setPage] = useState<Page>(() => bootstrap.termsGate || new URLSearchParams(location.search).get('setup')==='1' ? 'setup' : route(location.hash&&location.hash!=='#main'?location.hash:'#'+location.pathname));
  const documentPage=isDocumentPage(page);
  const model = useConsole(accepted, page), main = useRef<HTMLElement>(null), initial = useRef(true), guard=useRef<(() => Promise<boolean>) | undefined>(undefined);
  const currentPage=useRef(page), currentAccepted=useRef(accepted), navigation=useRef(0);currentPage.current=page;currentAccepted.current=accepted;
  const exitSetup=()=>{guard.current=undefined;location.hash='/home';setPage('home');};
  useEffect(() => {
    const params=new URLSearchParams(location.search), originalHash=location.hash;
    if(params.get('setup')==='1')params.delete('setup');
    if(bootstrap.termsGate||page==='setup')history.replaceState(null,'','/'+(params.size?'?'+params:'')+'#/setup');
    else if(isDocumentPage(location.pathname.slice(1)))history.replaceState(null,'','/'+(params.size?'?'+params:'')+(originalHash&&originalHash!=='#main'?originalHash:'#/'+location.pathname.slice(1)));
    const change = async () => {
      if(location.hash==='#main')return;
      if(!currentAccepted.current){history.replaceState(null,'','#/setup');setPage('setup');return;}
      const next=route(location.hash), revision=++navigation.current;
      if(currentPage.current==='setup'&&next!=='setup'&&guard.current&&!await guard.current()) {if(revision===navigation.current)history.replaceState(null,'','#/setup');return;}
      if(revision===navigation.current){setPage(next);setAnchor(location.hash.slice(1));}
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
  }, [page,anchor]);
  const s = model.status.data;
  useLayoutEffect(()=>{
    if((s || documentPage) && page!=='setup' && Object.hasOwn(anchors,anchor)){
      const target=document.getElementById(anchor);target?.focus();target?.scrollIntoView();
    }
  },[page,!!s,anchor,documentPage]);
  const feedback=<div className="app-feedback" hidden={documentPage}>
    <Feedback error={!!model.status.error}>{model.status.error ? '无法连接控制台，请检查程序是否仍在运行。' : ''}</Feedback><Feedback>{model.message}</Feedback>
    {s?.storageError && <Feedback error><p><strong>本地存储故障，已停止新的下载。</strong>已保存的本地视频仍可读取。</p><p>请检查磁盘空间和目录权限，修复后退出并重新打开整个应用。仅重新启停播放网关或队列无法恢复下载。</p><p>首个错误：{s.storageError}</p></Feedback>}
    {!s && accepted && page!=='setup' && !documentPage && !model.status.error && <Spinner label="正在读取控制台状态" />}
  </div>;
  return <ConsoleContext.Provider value={model}><div className="shell">
    <Link className="skip-link" href="#main" onClick={e => { e.preventDefault(); main.current?.focus(); }}>跳到主要内容</Link>
    <header hidden={page==='setup'}><Brand /><span role="status">{documentPage ? 'WannaDance 本地控制台' : model.stopped ? '应用正在退出或重启' : model.status.error ? '控制台连接中断，下方保留最后状态' : s ? '控制台已连接' : '正在连接…'}</span></header>
    <NavigationLayout items={pageLinks()} selected={page} hidden={page==='setup'}>
    <main ref={main} id="main" tabIndex={-1}><div className="page-heading" hidden={page==='setup'||documentPage}><Title2 as="h1">{pages[page]}</Title2>{s && <Badge color={s.running && s.hosts.ready ? 'success' : 'informative'}>{accelerationLabel(s)}</Badge>}</div><p className="page-description" hidden={page==='setup'||documentPage}>{descriptions[page]}</p>
      {page!=='setup'&&feedback}
      {s && <><div hidden={page !== 'home'}><Home visible={page==='home'}/></div><div hidden={page !== 'timeline'}><RequestTimeline visible={page==='timeline'}/></div><div hidden={page!=='library'}><Library visible={page==='library'} anchor={anchor}/></div><div hidden={page!=='settings'}><Settings visible={page==='settings'} anchor={anchor}/></div>
      </>}
      {page==='about'&&<AboutPage anchor={anchor} paths={s?{...bootstrap.paths,Storage:s.activeSettings.storageDir}:bootstrap.paths||{}}/>}
      {page==='setup'&&<Setup accepted={accepted} onAccepted={()=>setAccepted(true)} onExit={exitSetup} guard={guard} feedback={feedback}/>}
    </main><footer hidden={page==='setup'||documentPage}><p>关闭网页后程序仍在后台运行。{exitConnectionMessage(s)} <Link href="#preferences">查看退出设置</Link></p><Button disabled={model.unavailable} onClick={() => void model.action('exit')}>退出整个应用</Button></footer>
    <DocumentFooter page={page==='about'&&anchor.includes('help')?'help':page}/>
    </NavigationLayout>{model.dialog}
  </div></ConsoleContext.Provider>;
}
