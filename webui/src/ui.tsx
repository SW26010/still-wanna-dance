import { useEffect, useState, Fragment, type PropsWithChildren, type Ref, type FocusEventHandler, type ReactNode } from 'react';
import { FluentProvider, webLightTheme, webDarkTheme, Card, CardHeader, Divider, Image, Subtitle1, Subtitle2, Link, MessageBar, MessageBarBody, Accordion, AccordionItem, AccordionHeader, AccordionPanel, ProgressBar, NavDrawer, NavDrawerHeader, NavDrawerBody, NavItem, Hamburger, useRestoreFocusTarget } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';

export function Provider({ children }: PropsWithChildren) {
  const [dark, setDark] = useState(() => matchMedia('(prefers-color-scheme: dark)').matches);
  useEffect(() => {
    const media = matchMedia('(prefers-color-scheme: dark)');
    const change = () => setDark(media.matches);
    media.addEventListener('change', change);
    return () => media.removeEventListener('change', change);
  }, []);
  useEffect(() => {
    // Favicon rasterization/caching does not reliably follow media queries inside SVGs.
    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
    if (icon) icon.href = dark ? '/favicon.ico' : '/favicon-light.ico';
  }, [dark]);
  return <FluentProvider className="app-provider" theme={{ ...(dark ? webDarkTheme : webLightTheme), fontFamilyBase: '"Segoe UI", "Microsoft YaHei", sans-serif' }}>{children}</FluentProvider>;
}
export function Brand() {
  return <div className="brand"><Image src="/assets/logo.svg" className="brand-logo" width="32" height="32" shape="rounded" alt="" /><Subtitle1>Still Wanna Dance</Subtitle1></div>;
}
export const consolePages=[['home','游戏加速'],['library','下载管理'],['cache','本地视频'],['network','网络与代理'],['settings','设置']] as const;
export const documentPages={help:'帮助',about:'关于',terms:'使用条款'} as const;
export type DocumentPage=keyof typeof documentPages;
export const isDocumentPage=(page:string):page is DocumentPage=>Object.hasOwn(documentPages,page);
export function pageHref(page:typeof consolePages[number][0]|DocumentPage|'setup') {
  return bootstrap.page==='console'?'#/'+page:isDocumentPage(page)?'/'+page:'/#/'+page;
}
export const sectionHref=(id:string)=>(bootstrap.page==='console'?'#':'/#')+id;
type NavigationEntry={value:string;label:string;href?:string;disabled?:boolean;onClick?:()=>void};
export function NavigationLayout({children,items,selected,label='控制台页面',steps=false,hidden=false}:PropsWithChildren<{items:NavigationEntry[];selected:string;label?:string;steps?:boolean;hidden?:boolean}>) {
  const [narrow,setNarrow]=useState(()=>matchMedia('(max-width: 640px)').matches), [open,setOpen]=useState(false), [expanded,setExpanded]=useState(true);
  const restoreFocusTarget=useRestoreFocusTarget();
  useEffect(()=>{
    const media=matchMedia('(max-width: 640px)');
    const change=()=>{setNarrow(media.matches);setOpen(false);};
    media.addEventListener('change',change);return()=>media.removeEventListener('change',change);
  },[]);
  useEffect(()=>{setOpen(false);},[hidden]);
  return <div className="navigation-layout">
    <div className="navigation-sidebar" hidden={hidden}>
      <div className="navigation-toggle"><Hamburger {...restoreFocusTarget} aria-label={(!narrow&&expanded?'收起':'展开')+label} aria-expanded={narrow?open:expanded} onClick={()=>narrow?setOpen(true):setExpanded(value=>!value)}/></div>
      <NavDrawer className={narrow?undefined:'inline-navigation'} type={narrow?'overlay':'inline'} position="start" open={!hidden&&(narrow?open:expanded)} selectedValue={selected} onOpenChange={(_,data)=>setOpen(data.open)} onNavItemSelect={()=>setOpen(false)} aria-label={label}>
        {narrow&&<NavDrawerHeader><Hamburger aria-label={'收起'+label} onClick={()=>setOpen(false)}/></NavDrawerHeader>}
        <NavDrawerBody className="navigation-items">{items.map(item=><Fragment key={item.value}>{!steps&&item.value==='settings'&&<Divider className="navigation-divider"/>}<NavItem key={item.value} value={item.value} href={item.href} disabled={item.disabled} aria-current={selected===item.value?steps?'step':'page':undefined} onClick={item.onClick}>{item.label}</NavItem></Fragment>)}</NavDrawerBody>
      </NavDrawer>
    </div>
    <div className="page-content">{children}</div>
  </div>;
}
export function pageLinks() {
  return [...consolePages.map(([value,label])=>({value,label,href:pageHref(value)})),{value:'help',label:'帮助',href:pageHref('help')}];
}
export function Panel({ title, children, id, ref, onFocusCapture, action }: PropsWithChildren<{ title: string; id?: string; ref?:Ref<HTMLDivElement>; onFocusCapture?:FocusEventHandler<HTMLDivElement>; action?:ReactNode }>) {
  return <Card size="large" className="panel" id={id} ref={ref} onFocusCapture={onFocusCapture} tabIndex={id ? -1 : undefined}><CardHeader header={<Subtitle2 as="h2">{title}</Subtitle2>} action={action ? { children: action } : undefined} /><div className="content-stack">{children}</div></Card>;
}
export function Feedback({ children, error = false }: PropsWithChildren<{ error?: boolean }>) {
  return children ? <MessageBar intent={error ? 'error' : 'info'}><MessageBarBody>{children}</MessageBarBody></MessageBar> : null;
}
export function Details({ title, children, open = false, expanded:controlled, onToggle, keepMounted = false }: PropsWithChildren<{ title: string; open?: boolean; expanded?:boolean; onToggle?:(expanded:boolean)=>void; keepMounted?:boolean }>) {
  // Forms retain drafts while collapsed; anchor focus needs the final layout immediately.
  const [localExpanded,setExpanded]=useState(open), expanded=controlled??localExpanded;
  return <Accordion collapsible openItems={expanded?['detail']:[]} onToggle={(_,data)=>{const value=data.openItems.includes('detail');setExpanded(value);onToggle?.(value);}} onInvalidCapture={event=>{
    if(expanded)return;
    // Native form validation must reveal a collapsed field before focusing it.
    event.preventDefault();setExpanded(true);onToggle?.(true);
    const field=event.target as HTMLInputElement;
    requestAnimationFrame(()=>{if(field.isConnected)field.reportValidity();});
  }}><AccordionItem value="detail"><AccordionHeader>{title}</AccordionHeader><AccordionPanel collapseMotion={keepMounted?{children:(_,props)=>props.children}:undefined} hidden={keepMounted&&!expanded}><div className="content-stack">{children}</div></AccordionPanel></AccordionItem></Accordion>;
}
export function Progress({ value, max, label }: { value?: number; max?: number; label: string }) {
  return <ProgressBar aria-label={label} value={max ? Math.min(1, Math.max(0, (value || 0) / max)) : undefined} />;
}
export function DocumentLayout({ children }: PropsWithChildren) {
  return <div className="shell"><Link className="skip-link" href="#main">跳到主要内容</Link>
    <header><Brand /><span>WannaDance 本地控制台</span></header>
    <NavigationLayout items={pageLinks()} selected={isDocumentPage(bootstrap.page)?'help':bootstrap.page}>
    <main id="main" tabIndex={-1}>{children}</main>
    <DocumentFooter page={bootstrap.page}/>
    </NavigationLayout>
  </div>;
}
export function DocumentFooter({page}:{page:string}) {
  return page==='help'?<footer><Link href={pageHref('home')}>返回控制台</Link> · <Link href={pageHref('setup')}>重新进入引导</Link></footer>:null;
}
