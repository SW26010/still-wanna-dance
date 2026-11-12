import {useEffect,useRef,useState} from 'react';
import {Button,Dialog,DialogActions,DialogBody,DialogContent,DialogSurface,DialogTitle} from '@fluentui/react-components';
import {APIError,request} from './api';
import {Feedback} from './ui';

export type PreviewSelection={songID?:string;resource?:string;label:string};

export function LibraryPreview({storageID,target,onClose}:{storageID:string;target:PreviewSelection;onClose:()=>void}) {
  const video=useRef<HTMLVideoElement>(null);
  const [loading,setLoading]=useState('正在准备预览…'),[error,setError]=useState(''),[playHint,setPlayHint]=useState('');
  useEffect(()=>{
    const media=video.current!,controller=new AbortController();
    let current=true,retries=0,resumeAt=0,retryAt=0,restoring=false,shouldPlay=true;
    let retryTimer:ReturnType<typeof setTimeout>|undefined;
    setLoading('正在准备预览…');setError('');setPlayHint('');
    const play=async()=>{
      try {await media.play();}
      catch(e){if(current&&e instanceof DOMException&&e.name==='NotAllowedError'){setLoading('');setPlayHint('请点击播放器中的播放按钮。');}}
    };
    const onError=()=>{
      if(!current||!media.getAttribute('src')||retryTimer!==undefined)return;
      const code=media.error?.code;
      // Chromium also reports an HTTP failure before metadata as code 4.
      // A new native GET gives it a fresh need-by clock and reuses stored blocks.
      if((code===2||code===4)&&retries<3){
        if(!restoring)resumeAt=media.currentTime;
        retryAt=resumeAt;restoring=true;
        setError('');setPlayHint('');setLoading('正在重新连接视频…');
        retryTimer=setTimeout(()=>{
          retryTimer=undefined;
          if(!current)return;
          media.load();
          if(shouldPlay)void play();
        },1000*2**retries++);
        return;
      }
      setLoading('');
      setError(code===3?'浏览器无法解码此视频。':retries?'视频暂时无法播放，自动重试未成功，请关闭后重试。':'视频读取失败，请关闭后重试。');
    };
    const onMetadata=()=>{if(restoring){if(resumeAt>0)media.currentTime=resumeAt;restoring=false;}};
    const onTime=()=>{if(!restoring){resumeAt=media.currentTime;if(resumeAt>retryAt+1)retries=0;}};
    const onPlay=()=>{shouldPlay=true;};
    const onPause=()=>{if(!media.error)shouldPlay=false;};
    const onLoaded=()=>setLoading('');
    media.addEventListener('error',onError);
    media.addEventListener('loadedmetadata',onMetadata);
    media.addEventListener('timeupdate',onTime);
    media.addEventListener('play',onPlay);
    media.addEventListener('pause',onPause);
    media.addEventListener('loadeddata',onLoaded);
    void (async()=>{
      try {
        const {url}=await request<{url:string}>('library/preview',{storageID,songID:target.songID,resource:target.resource},AbortSignal.any([controller.signal,AbortSignal.timeout(15000)]));
        if(!current)return;
        media.src=url;
        await play();
      }catch(e){if(current){setLoading('');setError(e instanceof APIError?e.message:'无法准备预览，请关闭后重试。');}}
    })();
    return()=>{
      current=false;controller.abort();clearTimeout(retryTimer);
      media.removeEventListener('error',onError);
      media.removeEventListener('loadedmetadata',onMetadata);
      media.removeEventListener('timeupdate',onTime);
      media.removeEventListener('play',onPlay);
      media.removeEventListener('pause',onPause);
      media.removeEventListener('loadeddata',onLoaded);
      media.pause();media.removeAttribute('src');media.load();
    };
  },[storageID,target]);
  return <Dialog open onOpenChange={(_,data)=>{if(!data.open)onClose();}}>
    <DialogSurface className="library-preview"><DialogBody>
      <DialogTitle>预览 · <span className={target.label===target.resource?'resource-id':undefined}>{target.label}</span></DialogTitle>
      <DialogContent>
        <video ref={video} controls playsInline preload="none" aria-label="曲库预览播放器"/>
        {loading&&<p role="status">{loading}</p>}
        {playHint&&<p role="status">{playHint}</p>}
        <Feedback error>{error}</Feedback>
      </DialogContent>
      <DialogActions><Button onClick={onClose}>关闭预览</Button></DialogActions>
    </DialogBody></DialogSurface>
  </Dialog>;
}
