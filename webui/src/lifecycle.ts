import {useEffect,useRef} from 'react';

export type ShutdownAction = 'exit' | 'restart';
export const isShutdownAction=(value:unknown):value is ShutdownAction=>value==='exit'||value==='restart';

// Lifecycle is application-wide, including hidden/consent pages. Transport
// failure does not decide backend health; successful status reads retry it.
export function useLifecycle(stopped:boolean, status:unknown, receive:(action:ShutdownAction)=>void) {
  const receiver=useRef(receive),retry=useRef<()=>void>(()=>{});
  receiver.current=receive;
  useEffect(()=>{
    if(stopped)return;
    let socket:WebSocket|undefined,disposed=false;
    const disconnect=()=>{const current=socket;socket=undefined;current?.close();};
    const connect=()=>{
      if(disposed||socket)return;
      const url=new URL('/api/lifecycle',location.href);url.protocol=url.protocol==='https:'?'wss:':'ws:';
      const current=socket=new WebSocket(url);
      current.onmessage=event=>{
        if(socket!==current||disposed)return;
        try {
          const {action}=JSON.parse(event.data);
          if(isShutdownAction(action)){receiver.current(action);disconnect();}
          else if(action!=='running')disconnect();
        }catch{disconnect();}
      };
      current.onclose=()=>{if(socket===current)socket=undefined;};
    };
    retry.current=connect;
    connect();addEventListener('pagehide',disconnect);addEventListener('pageshow',connect);
    return()=>{disposed=true;retry.current=()=>{};disconnect();removeEventListener('pagehide',disconnect);removeEventListener('pageshow',connect);};
  },[stopped]);
  useEffect(()=>{if(status)retry.current();},[status]);
}
