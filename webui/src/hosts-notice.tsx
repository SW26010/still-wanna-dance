import {useEffect,useState} from 'react';
import {Notification} from './ui';

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
    body="在设置中关闭“退出时恢复游戏直连”并保存。下次玩游戏前仍需启动 SWD。"
    action={{href:'#restoreHostsOnExit',label:'打开退出设置'}}/>:null;
}
