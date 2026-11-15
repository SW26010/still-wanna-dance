import {useEffect,useRef} from 'react';
import {useResource} from './hooks';
import type {Download} from './types';

interface DownloadObservation {revision:number;tasks:Download[]}

// Idle reads are cheap (no task enrichment) and must keep waking external work.
// The server revision also catches flights that start and end between reads.
export function useDownloadObservation(enabled:boolean,revision:number,onChange:()=>void) {
  const result=useResource<DownloadObservation>('downloads',enabled,revision,5000,true);
  const previous=useRef<number|undefined>(undefined);
  const scope=useRef({enabled:false,revision});
  useEffect(()=>{
    const reset=()=>{previous.current=undefined;};
    document.addEventListener('visibilitychange',reset);
    return()=>document.removeEventListener('visibilitychange',reset);
  },[]);
  useEffect(()=>{
    if(scope.current.enabled!==enabled||scope.current.revision!==revision){
      scope.current={enabled,revision};previous.current=undefined;return;
    }
    if(!enabled||document.hidden)return;
    if(result.error)return;
    const next=result.data;
    if(!next)return;
    // The library can predate a whole flight even before the first snapshot.
    // Reconcile each new baseline too, including after visibility/scope resets.
    if(previous.current!==next.revision)onChange();
    previous.current=next.revision;
  },[enabled,revision,result.data,result.error,onChange]);
  return result;
}
