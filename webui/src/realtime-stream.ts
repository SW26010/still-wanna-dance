export type RealtimeConnection = 'connecting' | 'disconnected' | 'invalid' | 'paused';
interface Listener {
  // Returning false marks a terminal business event; throwing rejects bad data.
  receive:(event:string,data:any)=>void|boolean;
  connection:(state:RealtimeConnection)=>void;
}

// Owns one read-only subscription. Domain modules decide which views share it
// and how to apply snapshots/deltas; all transports use the same lifecycle.
export function subscribeRealtime(path:string,listener:Listener) {
  let socket:WebSocket|undefined,retry:ReturnType<typeof setTimeout>|undefined;
  let away=false,stopped=false;
  function disconnect() {
    clearTimeout(retry);retry=undefined;
    const current=socket;socket=undefined;current?.close();
  }
  function connect() {
    if(stopped||away||document.hidden||socket)return;
    clearTimeout(retry);retry=undefined;
    const url=new URL(path,location.href);url.protocol=url.protocol==='https:'?'wss:':'ws:';
    const current=socket=new WebSocket(url);
    listener.connection('connecting');
    current.onmessage=message=>{
      if(socket!==current)return;
      try {
        const value=JSON.parse(message.data);
        if(!value||typeof value.event!=='string'||!Object.hasOwn(value,'data'))throw new Error('invalid event');
        if(listener.receive(value.event,value.data)===false){stopped=true;disconnect();}
      }catch{stopped=true;disconnect();listener.connection('invalid');}
    };
    current.onclose=()=>{
      if(socket!==current)return;
      socket=undefined;listener.connection('disconnected');
      retry=setTimeout(connect,1000);
    };
  }
  function visibility() {
    if(document.hidden){disconnect();if(!stopped)listener.connection('paused');}else connect();
  }
  function pagehide() {away=true;disconnect();if(!stopped)listener.connection('paused');}
  function pageshow() {away=false;connect();}
  document.addEventListener('visibilitychange',visibility);
  addEventListener('pagehide',pagehide);addEventListener('pageshow',pageshow);
  if(document.hidden)listener.connection('paused');
  connect();
  return ()=>{
    stopped=true;disconnect();
    document.removeEventListener('visibilitychange',visibility);
    removeEventListener('pagehide',pagehide);removeEventListener('pageshow',pageshow);
  };
}
