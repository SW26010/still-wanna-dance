export type DiagnosticKind = 'connectivity' | 'calibration';
export interface DiagnosticState {revision:number;run:number;upstream:string;running:boolean;closed?:boolean;phase:string;finished?:string}
type Connection = 'connecting' | 'disconnected' | 'invalid' | 'paused';
interface Listener {receive:(state:DiagnosticState)=>void;connection:(state:Connection)=>void}

// Module scope is page scope. Views share the transport and fresh snapshots;
// action intents and errors remain local to each view.
const listeners:Record<DiagnosticKind,Set<Listener>>={connectivity:new Set(),calibration:new Set()};
const latest=new Map<DiagnosticKind,DiagnosticState>();
let socket:WebSocket|undefined,retry:ReturnType<typeof setTimeout>|undefined,away=false;
const active=()=>listeners.connectivity.size+listeners.calibration.size>0;
const notify=(state:Connection)=>{for(const group of Object.values(listeners))for(const listener of group)listener.connection(state);};
function disconnect() {
  clearTimeout(retry);retry=undefined;
  const current=socket;socket=undefined;latest.clear();current?.close();
}
function connect() {
  if(!active()||away||document.hidden||socket)return;
  clearTimeout(retry);retry=undefined;
  const url=new URL('/api/network/events',location.href);url.protocol=url.protocol==='https:'?'wss:':'ws:';
  const current=socket=new WebSocket(url);
  notify('connecting');
  current.onmessage=event=>{
    if(socket!==current)return;
    try {
      const {event:kind,data}=JSON.parse(event.data);
      if(kind!=='connectivity'&&kind!=='calibration')throw new Error('invalid event');
      const key:DiagnosticKind=kind;
      if(!data||!Number.isFinite(data.revision))throw new Error('invalid revision');
      if(data.revision<(latest.get(key)?.revision??-1))return;
      latest.set(key,data);
      for(const listener of listeners[key])listener.receive(data);
      if(latest.get('connectivity')?.closed&&latest.get('calibration')?.closed)disconnect();
    } catch {notify('invalid');disconnect();}
  };
  current.onclose=()=>{
    if(socket!==current)return;
    socket=undefined;latest.clear();notify('disconnected');
    retry=setTimeout(()=>{retry=undefined;connect();},1000);
  };
}
function visibility() {
  if(document.hidden){disconnect();notify('paused');}else connect();
}
function pagehide() {away=true;disconnect();notify('paused');}
function pageshow() {away=false;connect();}

export function subscribeDiagnostics(kind:DiagnosticKind,listener:Listener) {
  const first=!active();
  listeners[kind].add(listener);
  if(first){
    away=false;
    document.addEventListener('visibilitychange',visibility);
    addEventListener('pagehide',pagehide);addEventListener('pageshow',pageshow);
  }
  const state=latest.get(kind);
  if(state)listener.receive(state);
  else listener.connection(document.hidden?'paused':'connecting');
  connect();
  return ()=>{
    listeners[kind].delete(listener);
    if(!active()){
      disconnect();
      document.removeEventListener('visibilitychange',visibility);
      removeEventListener('pagehide',pagehide);removeEventListener('pageshow',pageshow);
    }
  };
}
