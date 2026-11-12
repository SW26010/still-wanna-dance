import {useEffect,useRef,useState,type ReactNode} from 'react';
import {Button,Card,CardFooter,CardPreview,Menu,MenuItem,MenuList,MenuPopover,MenuTrigger,Popover,PopoverSurface,Subtitle2,Text,Tooltip,useRestoreFocusTarget} from '@fluentui/react-components';
import {MoreHorizontalRegular,PlayCircleFilled} from '@fluentui/react-icons';
import {request,requestBlob} from './api';
import {categoryCover} from './category-covers';
import type {LibraryPage} from './types';

type Cover={source:string};

// Each page demand has four workers. The server also caps all tabs together at
// four network requests. Replacing/leaving a page cancels the old demand.
export function useLibraryCovers(ids:string,nextPage:string,enabled:boolean) {
  const [covers,setCovers]=useState<Record<string,Cover>>({});
  const [documentVisible,setDocumentVisible]=useState(!document.hidden);
  useEffect(()=>{const update=()=>setDocumentVisible(!document.hidden);document.addEventListener('visibilitychange',update);return()=>document.removeEventListener('visibilitychange',update);},[]);
  useEffect(()=>{
    if(!enabled||!documentVisible||!ids)return;
    const controller=new AbortController(),urls=new Set<string>();
    const current=ids.split(','),currentSet=new Set(current);
    setCovers({});
    const show=async(id:string,mode:string)=>{
      try {
        const blob=await requestBlob('library/cover?'+new URLSearchParams({id,...(mode?{mode}:{})}),controller.signal);
        if(controller.signal.aborted||!currentSet.has(id))return;
        const source=URL.createObjectURL(blob);urls.add(source);
        setCovers(previous=>({...previous,[id]:{source}}));
      }catch{/* Keep any existing image or category background on failure. */}
    };
    const workers=async(tasks:{id:string;mode:string}[])=>{
      let next=0;
      await Promise.all(Array.from({length:Math.min(4,tasks.length)},async()=>{
        while(!controller.signal.aborted){const task=tasks[next++];if(!task)break;await show(task.id,task.mode);}
      }));
    };
    void (async()=>{
      try {
        const {cached,refresh}=await request<{cached:string[];refresh:string[]}>('library/covers?'+new URLSearchParams({ids}),undefined,controller.signal);
        const available=new Set(cached);
        // Show existing current-page files first; these reads cannot download.
        await workers(current.filter(id=>available.has(id)).map(id=>({id,mode:'cached'})));
        await workers(current.filter(id=>!available.has(id)).map(id=>({id,mode:''})));
        // Only metadata is read for the next page, using the same search/sort.
        // Its failure must not prevent current images or their update checks.
        try {
          const next=nextPage?(await request<LibraryPage>(nextPage,undefined,controller.signal)).songs.map(song=>song.id):[];
          if(next.length&&!controller.signal.aborted){
            const state=await request<{cached:string[]}>('library/covers?'+new URLSearchParams({ids:next.join(',')}),undefined,controller.signal);
            const nextCached=new Set(state.cached);
            await workers(next.filter(id=>!currentSet.has(id)&&!nextCached.has(id)).map(id=>({id,mode:''})));
          }
        }catch{/* Optional next-page prefetch must not skip current-page checks. */}
        await workers(refresh.map(id=>({id,mode:'refresh'})));
      }catch{/* Cover metadata failure leaves loaded images and category backgrounds. */}
    })();
    return()=>{controller.abort();for(const url of urls)URL.revokeObjectURL(url);};
  },[ids,nextPage,enabled,documentVisible]);
  return enabled&&documentVisible?covers:{};
}

export function LibraryCoverCard({id,title,group,enabled,open,onOpenChange,cover,preferences,details,operations,previewDisabled,previewReason,onPreview}:{
  id:string;title:string;group:string;enabled:boolean;cover?:Cover;preferences:ReactNode;details:ReactNode;operations:ReactNode;
  open:boolean;onOpenChange:(open:boolean)=>void;
  previewDisabled:boolean;previewReason:string;onPreview:()=>void;
}) {
  const [menuOpen,setMenuOpen]=useState(false),[detailsAction,setDetailsAction]=useState(false);
  const [failedSource,setFailedSource]=useState<string>();
  const target=useRef<HTMLDivElement>(null),enterTimer=useRef<ReturnType<typeof setTimeout>>(undefined),leaveTimer=useRef<ReturnType<typeof setTimeout>>(undefined);
  const menuButton=useRef<HTMLButtonElement>(null),detailSurface=useRef<HTMLDivElement>(null);
  const clearEnter=()=>{clearTimeout(enterTimer.current);enterTimer.current=undefined;};
  const clearLeave=()=>{clearTimeout(leaveTimer.current);leaveTimer.current=undefined;};
  const setOpen=(value:boolean)=>{clearEnter();clearLeave();onOpenChange(value);};
  const leave=()=>{clearEnter();clearLeave();if(!detailsAction&&!menuOpen)leaveTimer.current=setTimeout(()=>setOpen(false),250);};
  const hover=(target:EventTarget)=>{
    if((target as Element).closest('.library-cover-policy,.library-cover-menu')){clearEnter();if(!detailsAction)setOpen(false);return;}
    clearLeave();
    if(enabled&&!menuOpen&&!detailsAction&&!open&&enterTimer.current===undefined)enterTimer.current=setTimeout(()=>setOpen(true),500);
  };
  const focusTarget=useRestoreFocusTarget();
  useEffect(()=>{if(!enabled){setOpen(false);setMenuOpen(false);}},[enabled]);
  useEffect(()=>{
    if(!open&&detailsAction){
      setDetailsAction(false);
      if(enabled&&(document.activeElement===document.body||detailSurface.current?.contains(document.activeElement)))menuButton.current?.focus();
    }
  },[open,detailsAction,enabled]);
  useEffect(()=>()=>{clearTimeout(enterTimer.current);clearTimeout(leaveTimer.current);},[]);
  useEffect(()=>{
    if(!open)return;
    const escape=(event:KeyboardEvent)=>{if(event.key==='Escape')setOpen(false);};
    document.addEventListener('keydown',escape);
    return()=>document.removeEventListener('keydown',escape);
  },[open]);
  const playButton=<Button {...focusTarget} className="library-cover-play" style={{backgroundImage:`url("${categoryCover(group)}")`}} appearance="transparent" aria-label={`预览歌曲 ${id}：${title}`} disabled={previewDisabled} disabledFocusable={!!previewReason} onClick={()=>{setOpen(false);onPreview();}}>
    {cover?.source&&cover.source!==failedSource&&<img key={cover.source} src={cover.source} alt="" width={480} height={360} onError={()=>setFailedSource(cover.source)}/>}
    <span className="library-cover-play-indicator" aria-hidden="true"><PlayCircleFilled/></span>
  </Button>;
  return <Popover unstable_disableAutoFocus={!detailsAction} open={enabled&&open&&!menuOpen} onOpenChange={(_,data)=>{if(!data.open)setOpen(false);}} positioning={{target:target.current,strategy:'fixed',autoSize:'height',overflowBoundaryPadding:16}}>
      <Card ref={target} className="library-cover-card" size="small" focusMode="off" role="group" aria-label={`歌曲 ${id}`} onMouseOver={event=>hover(event.target)} onMouseLeave={leave}>
        <CardPreview className="library-cover-preview"><div className="library-cover-media">
          {previewReason?<Tooltip content={previewReason} relationship="description">{playButton}</Tooltip>:playButton}
          <div className="library-cover-policy">{preferences}</div>
        </div></CardPreview>
        <CardFooter className="library-cover-footer" action={<Menu open={menuOpen} onOpenChange={(_,data)=>{clearLeave();setMenuOpen(data.open);if(data.open)setOpen(false);}}>
          <MenuTrigger disableButtonEnhancement><Button {...focusTarget} ref={menuButton} className="library-cover-menu" size="small" appearance="subtle" disabled={!enabled} icon={<MoreHorizontalRegular/>} aria-label={`歌曲 ${id} 的操作`}/></MenuTrigger>
          <MenuPopover><MenuList>{operations}<MenuItem onClick={()=>{clearLeave();setDetailsAction(true);setOpen(true);}}>详情</MenuItem></MenuList></MenuPopover>
        </Menu>}><span className="library-cover-id">{id}</span><Text className="library-cover-title" truncate wrap={false}>{title}</Text></CardFooter>
      </Card>
    <PopoverSurface ref={detailSurface} className="library-cover-details" aria-label={`歌曲 ${id} 的详情`} onMouseEnter={()=>{clearEnter();clearLeave();}} onMouseLeave={leave} onClick={event=>{if((event.target as HTMLElement).closest('a[href]'))setOpen(false);}}>
      <Subtitle2 as="h3">{title}</Subtitle2>{details}
    </PopoverSurface>
  </Popover>;
}
