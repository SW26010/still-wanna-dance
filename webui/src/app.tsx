import { useEffect, useRef, useState } from 'react';
import { Badge, Button, Link, Spinner, Title2 } from '@fluentui/react-components';
import { ConsoleContext, useConsole } from './hooks';
import { Brand, Feedback, NavigationLayout, pageLinks, documentPages, isDocumentPage, DocumentFooter } from './ui';
import { DocumentContent } from './documents';
import { Home, Library } from './home-library';
import { SettingsPage } from './settings';
import { Cache } from './cache';
import { Monitor } from './monitor';
import { Setup } from './setup';
import { bootstrap } from './bootstrap';
import { request } from './api';

const pages = {home:'开始使用',library:'准备歌曲',cache:'本地视频',monitor:'运行状态',settings:'设置',setup:'使用引导',...documentPages};
type Page = keyof typeof pages;
const descriptions:Partial<Record<Page,string>>={home:'为 VRChat WannaDance 复用已下载视频，也可提前准备房间队列。',monitor:'查看下载进度、最近请求和网络连接。',cache:'查找已下载的视频，清理磁盘空间。',library:'按房间队列提前准备，或批量下载缺失视频。',settings:'调整存储、网络和启动偏好。',setup:'设置、导入、检测和接入；可随时返回或离开。'};
const legacy: Record<string,Page> = {activation:'home',service:'home',queue:'library',overview:'cache',cache:'cache',downloads:'monitor',recent:'monitor',network:'monitor',batch:'library',preferences:'settings',prepareAheadSeconds:'settings',maxCacheGiB:'settings'};
export function route(hash: string): Page {
  if (hash === '#/upstream') return 'monitor';
  const key = hash.replace(/^#\/?/,'');
  return Object.hasOwn(pages,key) ? key as Page : legacy[key] || 'home';
}
export function App() {
  const [accepted,setAccepted]=useState(!bootstrap.termsGate);
  const [anchor,setAnchor]=useState(location.hash.slice(1));
  const [page, setPage] = useState<Page>(() => bootstrap.termsGate || new URLSearchParams(location.search).get('setup')==='1' ? 'setup' : route(location.hash&&location.hash!=='#main'?location.hash:'#'+location.pathname));
  const documentPage=isDocumentPage(page);
  const model = useConsole(accepted), main = useRef<HTMLElement>(null), initial = useRef(true), guard=useRef<(() => Promise<boolean>) | undefined>(undefined);
  const currentPage=useRef(page), currentAccepted=useRef(accepted), navigation=useRef(0);currentPage.current=page;currentAccepted.current=accepted;
  const exitSetup=()=>{guard.current=undefined;location.hash='/home';setPage('home');};
  useEffect(() => {
    const params=new URLSearchParams(location.search), originalHash=location.hash;
    if(params.get('setup')==='1')params.delete('setup');
    if(bootstrap.termsGate||page==='setup')history.replaceState(null,'','/'+(params.size?'?'+params:'')+'#/setup');
    else if(isDocumentPage(location.pathname.slice(1)))history.replaceState(null,'','/'+(params.size?'?'+params:'')+(originalHash&&originalHash!=='#main'?originalHash:'#/'+page));
    if(!bootstrap.termsGate&&!originalHash&&page==='home')void request<{state:string}>('setup').then(p=>{if(p.state==='active'&&!location.hash)location.hash='/setup';}).catch(()=>{});
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
  useEffect(() => {
    document.title = pages[page]+' · Still Wanna Dance';
    if (!initial.current) main.current?.focus();
    initial.current = false;
  }, [page]);
  const s = model.status.data;
  useEffect(()=>{
    if(s && page!=='setup' && Object.hasOwn(legacy,anchor)){
      const target=document.getElementById(anchor);target?.focus();target?.scrollIntoView();
    }
  },[page,!!s,anchor]);
  return <ConsoleContext.Provider value={model}><div className="shell">
    <Link className="skip-link" href="#main" onClick={e => { e.preventDefault(); main.current?.focus(); }}>跳到主要内容</Link>
    <header hidden={page==='setup'}><Brand /><span role="status">{documentPage ? 'WannaDance 本地控制台' : model.stopped ? '应用正在退出或重启' : model.status.error ? '控制台连接中断，下方保留最后状态' : s ? '控制台已连接' : '正在连接…'}</span></header>
    <NavigationLayout items={pageLinks()} selected={page} hidden={page==='setup'}>
    <main ref={main} id="main" tabIndex={-1}><div className="page-heading" hidden={page==='setup'||documentPage}><Title2 as="h1">{page === 'home' ? 'Load failed? But I still wanna dance!' : pages[page]}</Title2>{s && <Badge color={s.running && s.hosts.ready ? 'success' : 'informative'}>{s.running && s.hosts.ready ? '游戏接入：已完成' : s.running ? '游戏接入：等待配置' : '游戏接入：未启用'}</Badge>}</div><p className="page-description" hidden={page==='setup'||documentPage}>{descriptions[page]}</p>
      <div hidden={documentPage}>
      <Feedback error={!!model.status.error}>{model.status.error ? '无法连接控制台，请检查程序是否仍在运行。' : ''}</Feedback><Feedback>{model.message}</Feedback>
      {s?.storageError && <Feedback error><p><strong>本地存储故障，已停止新的下载。</strong>已保存的本地视频仍可读取。</p><p>请检查磁盘空间和目录权限，修复后退出并重新打开整个应用。仅重新启停播放网关或队列无法恢复下载。</p><p>首个错误：{s.storageError}</p></Feedback>}
      {!s && accepted && page!=='setup' && !documentPage && !model.status.error && <Spinner label="正在读取控制台状态" />}
      </div>
      {s && <><div hidden={page !== 'home'}><Home /></div><div hidden={page !== 'library'}><Library /></div><div hidden={page !== 'cache'}><Cache visible={page === 'cache'} /></div><div hidden={page !== 'monitor'}><Monitor visible={page === 'monitor'} /></div><div hidden={page !== 'settings'}><SettingsPage /></div>
      </>}
      {isDocumentPage(page)&&<DocumentContent page={page} paths={s?{...bootstrap.paths,Storage:s.activeSettings.storageDir}:bootstrap.paths} onAction={model.refresh}/>}
      {page==='setup'&&<Setup accepted={accepted} onAccepted={()=>setAccepted(true)} onExit={exitSetup} guard={guard}/>}
    </main><footer hidden={page==='setup'||documentPage}><div className="actions">{pageLinks().filter(item=>isDocumentPage(item.value)).map(item=><Link key={item.value} href={item.href}>{item.label}</Link>)}</div><p>关闭网页后程序仍在运行。退出前如需停用游戏接入，请先<Link href="#service">恢复游戏直连</Link>。</p><Button disabled={model.unavailable} onClick={() => void model.action('exit')}>退出整个应用</Button></footer>
    <DocumentFooter page={page}/>
    </NavigationLayout>{model.dialog}
  </div></ConsoleContext.Provider>;
}
