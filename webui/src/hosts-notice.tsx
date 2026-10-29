import {useEffect,useState} from 'react';
import {request} from './api';
import {useApp} from './hooks';
import {Notification} from './ui';

export function VideoImportSuggestion() {
  const app=useApp();
  const [dismissed,setDismissed]=useState(false),[pending,setPending]=useState(false),[error,setError]=useState('');
  async function acknowledge(navigate=false) {
    if(pending||app.unavailable)return;
    setPending(true);setError('');
    try {
      await request('import/dismiss-suggestion',{});
      setDismissed(true);app.refresh();
      if(navigate) {
        if(location.hash==='#video-import') {
          const target=document.getElementById('video-import');target?.focus({preventScroll:true});target?.scrollIntoView();
        } else location.hash='video-import';
      }
      else document.getElementById('main')?.focus({preventScroll:true});
    } catch {setError('未能关闭提示，请重试。');}
    finally {setPending(false);}
  }
  if(!app.status.data?.videoImportSuggested||dismissed||app.stopped)return null;
  return <Notification message="已有本地视频？" persistent
    body={<>可以导入 SWD 复用，减少重复下载。原视频会保留，同一分区无需重复存储。{error&&<p role="alert">{error}</p>}</>}
    action={{href:'#video-import',label:'前往导入',onClick:event=>{event.preventDefault();void acknowledge(true);}}}
    onDismiss={()=>void acknowledge()} dismissLabel="不再提示" disabled={pending||app.unavailable}/>;
}

export function HostsAuthorizationNotice({authorizationID}:{authorizationID?:string}) {
  const [noticeID,setNoticeID]=useState('');
  useEffect(()=>{
    if(!authorizationID){setNoticeID('');return;}
    // Acknowledge this authorization across reloads in the same tab.
    try {
      if(sessionStorage.getItem('swd-hosts-authorization')===authorizationID)return;
      sessionStorage.setItem('swd-hosts-authorization',authorizationID);
    }catch{ /* Storage is optional; keep the notice and in-page deduplication. */ }
    setNoticeID(authorizationID);
  },[authorizationID]);
  return noticeID&&authorizationID?<Notification key={noticeID}
    message="可在退出后保留游戏接入"
    body="在设置中关闭“停止加速或退出时恢复游戏直连”并保存。下次玩游戏前仍需启动 SWD。"
    action={{href:'#restoreHostsOnExit',label:'打开退出设置'}}/>:null;
}
