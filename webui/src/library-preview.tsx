import {useEffect,useRef,useState} from 'react';
import {Button,Dialog,DialogActions,DialogBody,DialogContent,DialogSurface,DialogTitle} from '@fluentui/react-components';
import {APIError,request} from './api';
import {Feedback} from './ui';

export type PreviewSelection={songID?:string;resource?:string;label:string};

export function LibraryPreview({storageID,target,onClose}:{storageID:string;target:PreviewSelection;onClose:()=>void}) {
  const video=useRef<HTMLVideoElement>(null);
  const [loading,setLoading]=useState(true),[error,setError]=useState(''),[playHint,setPlayHint]=useState('');
  useEffect(()=>{
    const media=video.current!,controller=new AbortController();
    let current=true;
    setLoading(true);setError('');setPlayHint('');
    void (async()=>{
      try {
        const {url}=await request<{url:string}>('library/preview',{storageID,songID:target.songID,resource:target.resource},AbortSignal.any([controller.signal,AbortSignal.timeout(15000)]));
        if(!current)return;
        media.src=url;
        try {await media.play();}
        catch(e){if(current&&e instanceof DOMException&&e.name==='NotAllowedError'){setLoading(false);setPlayHint('请点击播放器中的播放按钮。');}}
      }catch(e){if(current){setLoading(false);setError(e instanceof APIError?e.message:'无法准备预览，请关闭后重试。');}}
    })();
    return()=>{current=false;controller.abort();media.pause();media.removeAttribute('src');media.load();};
  },[storageID,target]);
  return <Dialog open onOpenChange={(_,data)=>{if(!data.open)onClose();}}>
    <DialogSurface className="library-preview"><DialogBody>
      <DialogTitle>预览 · {target.label}</DialogTitle>
      <DialogContent>
        <video ref={video} controls playsInline preload="none" aria-label="曲库预览播放器" onLoadedData={()=>setLoading(false)} onError={event=>{
          if(!event.currentTarget.getAttribute('src'))return;
          setLoading(false);setError(event.currentTarget.error?.code===3||event.currentTarget.error?.code===4?'浏览器无法播放此视频，可能是资源不可用或编码不受支持。':'视频读取失败，请关闭后重试。');
        }}/>
        {loading&&<p role="status">正在准备预览…</p>}
        {playHint&&<p role="status">{playHint}</p>}
        <Feedback error>{error}</Feedback>
      </DialogContent>
      <DialogActions><Button onClick={onClose}>关闭预览</Button></DialogActions>
    </DialogBody></DialogSurface>
  </Dialog>;
}
