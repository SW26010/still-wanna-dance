import {useEffect,useRef,useState} from 'react';
import {useApp} from './hooks';
import {Notification} from './ui';

export function SpeedTestSuggestion() {
  const app=useApp(),checked=useRef(false);
  const [show,setShow]=useState(false);
  const ready=!!app.status.data&&!app.status.error&&!app.stopped;
  useEffect(()=>{
    if(!ready||checked.current)return;
    checked.current=true;
    try {
      if(localStorage.getItem('swd-speed-test-suggestion'))return;
      localStorage.setItem('swd-speed-test-suggestion','shown');
    }catch{ /* Optional browser storage; keep the suggestion once in this page. */ }
    setShow(true);
  },[ready]);
  return show&&!app.stopped?<Notification message="建议先测试下载速度" persistent
    body={<>了解当前环境的下载表现。<br/>建议使用日常游戏网络环境，暂停其他下载后运行测试。</>}
    action={{href:'#network-tests',label:'前往测速'}}/>:null;
}
