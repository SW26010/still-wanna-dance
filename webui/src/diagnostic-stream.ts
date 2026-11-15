import {subscribeRealtime,type RealtimeConnection} from './realtime-stream';

export type DiagnosticKind = 'connectivity' | 'calibration';
export interface DiagnosticState {revision:number;run:number;upstream:string;running:boolean;closed?:boolean;phase:string;finished?:string}
interface Listener {receive:(state:DiagnosticState)=>void;connection:(state:RealtimeConnection)=>void}

// Module scope is page scope. Views share the transport and fresh snapshots;
// action intents and errors remain local to each view.
const listeners:Record<DiagnosticKind,Set<Listener>>={connectivity:new Set(),calibration:new Set()};
const latest=new Map<DiagnosticKind,DiagnosticState>();
let release:(()=>void)|undefined;
const active=()=>listeners.connectivity.size+listeners.calibration.size>0;
function connect() {
  release=subscribeRealtime('/api/network/events',{
    receive:(kind,data)=>{
      if(kind!=='connectivity'&&kind!=='calibration')throw new Error('invalid event');
      const key:DiagnosticKind=kind;
      if(!data||!Number.isFinite(data.revision))throw new Error('invalid revision');
      if(data.revision<(latest.get(key)?.revision??-1))return;
      latest.set(key,data);
      for(const listener of listeners[key])listener.receive(data);
      if(latest.get('connectivity')?.closed&&latest.get('calibration')?.closed)return false;
    },
    connection:state=>{
      latest.clear();
      for(const group of Object.values(listeners))for(const listener of group)listener.connection(state);
    },
  });
}
export function subscribeDiagnostics(kind:DiagnosticKind,listener:Listener) {
  const first=!active();
  listeners[kind].add(listener);
  const state=latest.get(kind);
  if(state)listener.receive(state);
  else listener.connection(document.hidden?'paused':'connecting');
  if(first)connect();
  return ()=>{
    listeners[kind].delete(listener);
    if(!active()){
      release?.();release=undefined;latest.clear();
    }
  };
}
