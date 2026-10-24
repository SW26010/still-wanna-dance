import {useLayoutEffect,useMemo,useState} from 'react';

// Each visit owns its writers. Leaving, replacing the storage scope or unmounting
// invalidates late feedback without canceling the operation or releasing its lock.
export function usePageFeedback<T>(scope:string|undefined) {
  const visit=useMemo(()=>({active:false}),[scope]);
  const [feedback,setFeedback]=useState<{scope:string;value:T}>();
  useLayoutEffect(()=>{
    visit.active=scope!==undefined;
    setFeedback(current=>current?.scope===scope?current:undefined);
    return()=>{visit.active=false;};
  },[scope,visit]);
  const setValue=(value:T|undefined,destination=scope)=>{
    // An explicit destination supports the wizard's completion handoff to home.
    if(visit.active&&destination!==undefined)setFeedback(value===undefined?undefined:{scope:destination,value});
  };
  return {value:feedback?.scope===scope?feedback?.value:undefined,setValue};
}
