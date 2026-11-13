import {useEffect,useRef,useState,type ReactNode} from 'react';
import {flushSync} from 'react-dom';
import {Button,Card,CardFooter,CardPreview,Menu,MenuItem,MenuList,MenuPopover,MenuTrigger,Popover,PopoverSurface,Subtitle2,Text,Tooltip,useRestoreFocusTarget} from '@fluentui/react-components';
import {MoreHorizontalRegular,PlayCircleFilled} from '@fluentui/react-icons';
import {request,requestBlob} from './api';
import {categoryCover} from './category-covers';
import type {LibraryCoverChoice,LibraryPage} from './types';

type Cover={source?:string;unavailable?:boolean};
type CachedCover={source:string;bytes:number};
const retainedCoverLimit=150,retainedCoverBudget=8*1024*1024;

// Each page demand has four workers. The server also caps all tabs together at
// four network requests. Replacing/leaving a page cancels the old demand.
export function useLibraryCovers(page:LibraryPage|undefined,nextPage:string,enabled:boolean) {
  const demand=JSON.stringify(page?.covers?{ids:page.songs.map(song=>song.id),choices:page.covers}:null);
  const [covers,setCovers]=useState<{demand:string;images:Record<string,Cover>}>({demand:'',images:{}});
  const cached=useRef(new Map<string,CachedCover>()),urls=useRef(new Set<string>());
  const [documentVisible,setDocumentVisible]=useState(!document.hidden);
  useEffect(()=>{const update=()=>setDocumentVisible(!document.hidden);document.addEventListener('visibilitychange',update);return()=>document.removeEventListener('visibilitychange',update);},[]);
  useEffect(()=>()=>{for(const url of urls.current)URL.revokeObjectURL(url);urls.current.clear();cached.current.clear();},[]);
  useEffect(()=>{
    // Retire replaced/evicted URLs after React has committed their replacements.
    const retained=new Set([...cached.current.values(),...Object.values(covers.images)].map(cover=>cover.source));
    for(const url of urls.current)if(!retained.has(url)){URL.revokeObjectURL(url);urls.current.delete(url);}
  },[covers]);
  useEffect(()=>{
    if(!enabled||!documentVisible||demand==='null')return;
    const controller=new AbortController(),cache=cached.current;
    const {ids:current,choices:selection}:{ids:string[];choices:Record<string,LibraryCoverChoice>}=JSON.parse(demand);
    const currentSet=new Set(current),unavailable=new Set<string>();
    const publish=()=>{
      // Current-page images are the active working set; budget retained others.
      let bytes=[...cache].reduce((total,[id,cover])=>total+(currentSet.has(id)?0:cover.bytes),0);
      for(const [id,cover] of cache){
        if(cache.size<=retainedCoverLimit&&bytes<=retainedCoverBudget)break;
        if(!currentSet.has(id)){bytes-=cover.bytes;cache.delete(id);}
      }
      const images:Record<string,Cover>=Object.fromEntries(cache);
      for(const id of unavailable)if(!images[id])images[id]={unavailable:true};
      setCovers({demand,images});
    };
    for(const id of current){
      if(selection[id].source==='category'){cache.delete(id);continue;}
      const cover=cache.get(id);if(cover){cache.delete(id);cache.set(id,cover);}
    }
    publish();
    const show=async(id:string,mode:string)=>{
      let pending:string|undefined;
      try {
        if(mode!=='refresh'&&cache.has(id))return;
        const blob=await requestBlob('library/cover?'+new URLSearchParams({id,...(mode?{mode}:{})}),controller.signal);
        if(controller.signal.aborted)return;
        const source=URL.createObjectURL(blob);pending=source;
        const image=new Image();image.src=source;await image.decode();
        if(controller.signal.aborted)return;
        urls.current.add(source);pending=undefined;
        cache.delete(id);cache.set(id,{source,bytes:blob.size});
        publish();
      }catch{
        if(!controller.signal.aborted&&currentSet.has(id)&&selection[id].source==='local'&&mode!=='refresh'&&!cache.has(id)){unavailable.add(id);publish();}
      }
      finally{if(pending)URL.revokeObjectURL(pending);}
    };
    const workers=async(tasks:{id:string;mode:string}[])=>{
      let next=0;
      await Promise.all(Array.from({length:Math.min(4,tasks.length)},async()=>{
        while(!controller.signal.aborted){const task=tasks[next++];if(!task)break;await show(task.id,task.mode);}
      }));
    };
    void (async()=>{
      try {
        // Show existing current-page files first; these reads cannot download.
        await workers(current.filter(id=>selection[id].source==='local').map(id=>({id,mode:'cached'})));
        await workers(current.filter(id=>selection[id].source==='category').map(id=>({id,mode:''})));
        // Prepare next-page files and decoded images using the same search/sort.
        // Cached reads cannot download; failures must not skip current updates.
        try {
          const next=nextPage?await request<LibraryPage>(nextPage,undefined,controller.signal):undefined;
          if(next?.covers&&!controller.signal.aborted){
            const choices=next.covers,ids=next.songs.map(song=>song.id).filter(id=>!currentSet.has(id));
            for(const id of ids)if(choices[id].source==='category')cache.delete(id);
            publish();
            await workers(ids.map(id=>({id,mode:choices[id].source==='local'?'cached':''})));
          }
        }catch{/* Optional next-page prefetch must not skip current-page checks. */}
        await workers(current.filter(id=>selection[id].refresh).map(id=>({id,mode:'refresh'})));
      }catch{/* Failed work leaves loaded images or the selected category source. */}
    })();
    return()=>controller.abort();
  },[demand,nextPage,enabled,documentVisible]);
  // A new page can use retained local images during its first render. Stale
  // failures and images whose selected source changed still belong to the old page.
  return covers.demand===demand?covers.images:Object.fromEntries(Object.entries(covers.images).filter(([id,cover])=>page?.covers?.[id]?.source==='local'&&cover.source));
}

function CoverImage({source,fallback}:{source?:string;fallback:string}) {
  const [displayed,setDisplayed]=useState<string>(),[failed,setFailed]=useState<string>();
  const candidate=source&&source===failed?fallback:source;
  // Keep the shown image during updates; React retains the loaded candidate by key.
  return [...new Set([displayed,candidate])].filter((url):url is string=>!!url).map(url=><img key={url}
    // Cached images can already be complete when mounted. Commit their visible
    // state before paint instead of waiting for a later load-event render.
    ref={image=>{
      if(!image||url!==candidate||url===displayed)return;
      if(image.complete&&image.naturalWidth){setDisplayed(url);return;}
      // Cached loading can complete after this commit but before the first
      // paint, ahead of the native load event. Check once in that same frame.
      const frame=requestAnimationFrame(()=>{if(image.complete&&image.naturalWidth)flushSync(()=>setDisplayed(url));});
      return()=>cancelAnimationFrame(frame);
    }}
    src={url} alt="" width={480} height={360} className={url===displayed?'is-ready':undefined}
    onLoad={()=>{if(url===candidate&&url!==displayed)flushSync(()=>setDisplayed(url));}} onError={()=>{if(url===candidate)setFailed(url);}}/>);
}

export function LibraryCoverCard({id,title,group,selection,enabled,open,onOpenChange,cover,preferences,details,operations,previewDisabled,previewReason,onPreview}:{
  id:string;title:string;group:string;selection?:LibraryCoverChoice['source'];enabled:boolean;cover?:Cover;preferences:ReactNode;details:ReactNode;operations:ReactNode;
  open:boolean;onOpenChange:(open:boolean)=>void;
  previewDisabled:boolean;previewReason:string;onPreview:()=>void;
}) {
  const [menuOpen,setMenuOpen]=useState(false),[detailsAction,setDetailsAction]=useState(false);
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
  const fallback=categoryCover(group),source=cover?.source||(selection==='category'||cover?.unavailable?fallback:undefined);
  const playButton=<Button {...focusTarget} className="library-cover-play" appearance="transparent" aria-label={`预览歌曲 ${id}：${title}`} disabled={previewDisabled} disabledFocusable={!!previewReason} onClick={()=>{setOpen(false);onPreview();}}>
    <CoverImage source={source} fallback={fallback}/>
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
