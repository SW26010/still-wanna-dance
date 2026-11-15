import {useEffect,useRef} from 'react';
import {useResource} from './hooks';
import type {Download} from './types';

interface DownloadObservation {revision:number;tasks:Download[]}

// Idle reads are cheap (no task enrichment) and must keep waking external work.
// The server revision also catches flights that start and end between reads.
export function useDownloadObservation(enabled:boolean,revision:number,onChange:()=>void) {
  const result=useResource<DownloadObservation>('downloads',enabled,revision,5000,true);
  const previous=useRef<number|null|undefined>(undefined);
  const scope=useRef({enabled:false,revision});
  useEffect(()=>{
    const reset=()=>{previous.current=result.error?null:undefined;};
    document.addEventListener('visibilitychange',reset);
    return()=>document.removeEventListener('visibilitychange',reset);
  },[result.error]);
  useEffect(()=>{
    if(scope.current.enabled!==enabled||scope.current.revision!==revision){
      // Entry/save reloads the page, but failed observation still needs recovery.
      scope.current={enabled,revision};previous.current=result.error?null:undefined;return;
    }
    if(!enabled||document.hidden)return;
    if(result.error){
      // With no successful baseline, recovery must reconcile the library too.
      if(previous.current===undefined)previous.current=null;
      return;
    }
    const next=result.data;
    if(!next)return;
    if(previous.current!==undefined&&previous.current!==next.revision)onChange();
    previous.current=next.revision;
  },[enabled,revision,result.data,result.error,onChange]);
  return result;
}
