import { createContext, useContext, useEffect, useRef, useState, Fragment, type PropsWithChildren, type Ref, type FocusEventHandler, type ReactNode, type MouseEventHandler } from 'react';
import { FluentProvider, webLightTheme, webDarkTheme, Card, CardHeader, Divider, Image, Subtitle1, Subtitle2, Link, MessageBar, MessageBarBody, ProgressBar, NavDrawer, NavDrawerHeader, NavDrawerBody, NavItem, Hamburger, useRestoreFocusTarget } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';
import { Badge, Button, Tooltip, Toaster, Toast, ToastTitle, ToastBody, ToastFooter, ToastTrigger, useToastController, useId, Popover, PopoverTrigger, PopoverSurface } from '@fluentui/react-components';
import { InfoRegular, DismissRegular, CheckmarkCircleRegular, DismissCircleRegular, WarningRegular, ClockRegular, SubtractCircleRegular } from '@fluentui/react-icons';

import type { ThemePreference } from './types';
const ThemeContext=createContext<{preference:ThemePreference;setPreference:(value:ThemePreference)=>void}>({preference:'system',setPreference:()=>{}});
export const useThemePreference=()=>useContext(ThemeContext);
export function Provider({ children }: PropsWithChildren) {
  const [preference,setPreference]=useState<ThemePreference>(bootstrap.theme ?? 'system');
  const [systemDark, setSystemDark] = useState(() => matchMedia('(prefers-color-scheme: dark)').matches);
  const dark=preference==='dark'||preference==='system'&&systemDark;
  useEffect(() => {
    const media = matchMedia('(prefers-color-scheme: dark)');
    const change = () => setSystemDark(media.matches);
    media.addEventListener('change', change);
    return () => media.removeEventListener('change', change);
  }, []);
  useEffect(() => {
    // Favicon rasterization/caching does not reliably follow media queries inside SVGs.
    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
    if (icon) icon.href = dark ? '/favicon.ico' : '/favicon-light.ico';
  }, [dark]);
  return <ThemeContext.Provider value={{preference,setPreference}}><FluentProvider className="app-provider" data-theme={dark?'dark':'light'} style={{colorScheme:dark?'dark':'light'}} theme={{ ...(dark ? webDarkTheme : webLightTheme), fontFamilyBase: '"Segoe UI", "Microsoft YaHei", sans-serif' }}>{children}<Toaster toasterId="app-notices" position="bottom-end" limit={4} pauseOnHover pauseOnWindowBlur /></FluentProvider></ThemeContext.Provider>;
}
export function Notification({message,body,action,error=false,persistent=false,onDismiss,dismissLabel,disabled=false}: {message?:string;body?:ReactNode;action?:{href:string;label:string;onClick?:MouseEventHandler<HTMLAnchorElement>};error?:boolean;persistent?:boolean;onDismiss?:()=>void;dismissLabel?:string;disabled?:boolean}) {
  const toastId=useId('notice'),{dispatchToast,dismissToast,updateToast}=useToastController('app-notices');
  const href=action?.href,label=action?.label,onAction=action?.onClick;
  const close=<Button appearance="transparent" size="small" icon={<DismissRegular/>} aria-label="关闭通知" disabled={disabled} onClick={onDismiss}/>;
  const link=<Link href={href} disabled={disabled} onClick={onAction}>{label}</Link>;
  const content=<Toast>
    <ToastTitle action={onDismiss?close:<ToastTrigger>{close}</ToastTrigger>}>{message}</ToastTitle>
    {body&&<ToastBody>{body}</ToastBody>}
    {(href&&label||dismissLabel)&&<ToastFooter>{href&&label&&(onAction?link:<ToastTrigger>{link}</ToastTrigger>)}{dismissLabel&&<Button appearance="transparent" size="small" disabled={disabled} onClick={onDismiss}>{dismissLabel}</Button>}</ToastFooter>}
  </Toast>;
  const latest=useRef(content),current=useRef<string|undefined>(undefined);latest.current=content;
  useEffect(()=>{
    if(!message)return;
    const id=toastId+'-'+crypto.randomUUID();current.current=id;
    dispatchToast(latest.current,{toastId:id,intent:error?'error':'info',timeout:error||persistent?-1:7000});
    return()=>{current.current=undefined;dismissToast(id);};
  },[message,error,persistent,toastId,dispatchToast,dismissToast]);
  // Update pending controls and body in place without restarting the notice or
  // replacing the focused action on every status refresh.
  useEffect(()=>{
    if(current.current)updateToast({toastId:current.current,content:latest.current});
  },[body,href,label,onAction,onDismiss,dismissLabel,disabled,updateToast]);
  return null;
}
export function Hint({children,label='说明'}:PropsWithChildren<{label?:string}>) {
  return <Tooltip content={<>{children}</>} relationship="description"><Button size="small" appearance="transparent" icon={<InfoRegular/>} aria-label={label}/></Tooltip>;
}
export function RecordDetails({title,children}:PropsWithChildren<{title:string}>) {
  const [open,setOpen]=useState(false);
  return <Popover open={open} onOpenChange={(_,data)=>setOpen(data.open)} positioning={{strategy:'fixed',autoSize:'height',shiftToCoverTarget:true,overflowBoundaryPadding:16}}><PopoverTrigger disableButtonEnhancement><Button size="small" appearance="subtle" aria-label={title}>详情</Button></PopoverTrigger><PopoverSurface aria-label={title} onClick={event=>{if((event.target as HTMLElement).closest('a[href]'))setOpen(false);}}><Subtitle2 as="h3">{title}</Subtitle2>{children}</PopoverSurface></Popover>;
}
const statusIcons={success:CheckmarkCircleRegular,danger:DismissCircleRegular,warning:WarningRegular,brand:ClockRegular,informative:SubtractCircleRegular};
export function StatusBadge({label,color,children,description}:PropsWithChildren<{label?:string;color:keyof typeof statusIcons;description?:string}>) {
  const Icon=statusIcons[color];
  const badge=<Badge size="large" appearance="tint" color={color} icon={<Icon aria-hidden="true"/>} role="status" aria-label={label} aria-atomic="true" tabIndex={description?0:undefined}>{label&&<>{label}：</>}{children}</Badge>;
  return description?<Tooltip content={description} relationship="description">{badge}</Tooltip>:badge;
}
export function TableRegion({children,label}:PropsWithChildren<{label:string}>) {
  return <div className="table-region" role="region" aria-label={label} tabIndex={0}>{children}</div>;
}
export function Brand() {
  return <Link className="brand" href={pageHref('home')} appearance="subtle" aria-label="Still Wanna Dance，返回首页"><Image src="/assets/logo.svg" className="brand-logo" width="32" height="32" shape="rounded" alt="" /><Subtitle1>Still Wanna Dance</Subtitle1></Link>;
}
export const consolePages=[['home','加速'],['timeline','历史'],['library','曲库'],['network','网络'],['settings','设置']] as const;
export const documentPages={help:'帮助',about:'关于',terms:'使用条款'} as const;
export type DocumentPage=keyof typeof documentPages;
export const isDocumentPage=(page:string):page is DocumentPage=>Object.hasOwn(documentPages,page);
export function pageHref(page:typeof consolePages[number][0]|DocumentPage) {
  return bootstrap.page==='console'?'#/'+page:isDocumentPage(page)?'/'+page:'/#/'+page;
}
export const sectionHref=(id:string)=>(bootstrap.page==='console'?'#':'/#')+id;
type NavigationEntry={value:string;label:string;href?:string;disabled?:boolean;onClick?:()=>void};
export function NavigationLayout({children,items,selected,label='页面导航'}:PropsWithChildren<{items:NavigationEntry[];selected:string;label?:string}>) {
  const [narrow,setNarrow]=useState(()=>matchMedia('(max-width: 640px)').matches), [open,setOpen]=useState(false), [expanded,setExpanded]=useState(true);
  const restoreFocusTarget=useRestoreFocusTarget();
  useEffect(()=>{
    const media=matchMedia('(max-width: 640px)');
    const change=()=>{setNarrow(media.matches);setOpen(false);};
    media.addEventListener('change',change);return()=>media.removeEventListener('change',change);
  },[]);
  return <div className="navigation-layout">
    <div className="navigation-sidebar">
      <div className="navigation-toggle"><Hamburger {...restoreFocusTarget} aria-label={(!narrow&&expanded?'收起':'展开')+label} aria-expanded={narrow?open:expanded} onClick={()=>narrow?setOpen(true):setExpanded(value=>!value)}/></div>
      <NavDrawer className={narrow?undefined:'inline-navigation'} type={narrow?'overlay':'inline'} position="start" open={narrow?open:expanded} selectedValue={selected} onOpenChange={(_,data)=>setOpen(data.open)} onNavItemSelect={()=>setOpen(false)} aria-label={label}>
        {narrow&&<NavDrawerHeader><Hamburger aria-label={'收起'+label} onClick={()=>setOpen(false)}/></NavDrawerHeader>}
        <NavDrawerBody className="navigation-items">{items.map(item=><Fragment key={item.value}>{item.value==='settings'&&<Divider className="navigation-divider"/>}<NavItem key={item.value} value={item.value} href={item.href} disabled={item.disabled} aria-current={selected===item.value?'page':undefined} onClick={item.onClick}>{item.label}</NavItem></Fragment>)}</NavDrawerBody>
      </NavDrawer>
    </div>
    <div className="page-content">{children}</div>
  </div>;
}
export function pageLinks() {
  return [...consolePages.map(([value,label])=>({value,label,href:pageHref(value)})),{value:'about',label:'关于',href:pageHref('about')}];
}
export function Panel({ title, children, id, ref, onFocusCapture, action }: PropsWithChildren<{ title: string; id?: string; ref?:Ref<HTMLDivElement>; onFocusCapture?:FocusEventHandler<HTMLDivElement>; action?:ReactNode }>) {
  return <Card size="large" className="panel" id={id} ref={ref} onFocusCapture={onFocusCapture} tabIndex={id ? -1 : undefined}><CardHeader header={<Subtitle2 as="h2">{title}</Subtitle2>} action={action ? { children: action } : undefined} /><div className="content-stack">{children}</div></Card>;
}
export function Feedback({ children, error = false }: PropsWithChildren<{ error?: boolean }>) {
  return children ? <MessageBar layout="multiline" intent={error ? 'error' : 'info'}><MessageBarBody>{children}</MessageBarBody></MessageBar> : null;
}
export function Progress({ value, max, label }: { value?: number; max?: number; label: string }) {
  return <ProgressBar aria-label={label} value={max ? Math.min(1, Math.max(0, (value || 0) / max)) : undefined} />;
}
export function DocumentLayout({ children }: PropsWithChildren) {
  return <div className="shell"><Link className="skip-link" href="#main">跳到主要内容</Link>
    <header><Brand /><span>SWD WebUI</span></header>
    <NavigationLayout items={pageLinks()} selected={isDocumentPage(bootstrap.page)?'about':bootstrap.page}>
    <main id="main" tabIndex={-1}>{children}</main>
    <DocumentFooter page={bootstrap.page}/>
    </NavigationLayout>
  </div>;
}
export function DocumentFooter({page}:{page:string}) {
  return page==='help'?<footer><Link href={pageHref('home')}>返回首页</Link></footer>:null;
}
