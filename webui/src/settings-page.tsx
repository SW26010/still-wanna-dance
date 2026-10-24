import {useLayoutEffect,useState} from 'react';
import {Tab,TabList} from '@fluentui/react-components';
import {SettingsPage,type SettingsSection} from './settings';
import {Network} from './network';

export function settingsTab(anchor:string):SettingsSection {
  const key=anchor.replace(/^\//,'').split('?')[0];
  if(['storage-settings','maxCacheGiB'].includes(key))return 'storage';
  if(['download-settings','prepareAheadSeconds'].includes(key))return 'downloads';
  if(['network','network-proxy','network-access','service','upstream','monitor'].includes(key))return 'network';
  return 'application';
}
export function Settings({visible,anchor}:{visible:boolean;anchor:string}) {
  const [tab,setTab]=useState<SettingsSection>(()=>settingsTab(anchor));
  useLayoutEffect(()=>{if(visible){setTab(settingsTab(anchor));const frame=requestAnimationFrame(()=>{const target=document.getElementById(anchor);target?.focus();target?.scrollIntoView();});return()=>cancelAnimationFrame(frame);}},[visible,anchor]);
  return <><TabList aria-label="设置分类" selectedValue={tab} onTabSelect={(_,v)=>location.hash=({application:'/settings',storage:'storage-settings',downloads:'download-settings',network:'network'} as const)[v.value as SettingsSection]}>{Object.entries({application:'常规',storage:'存储',downloads:'预加载与下载',network:'网络'}).map(([value,label])=><Tab id={'settings-tab-'+value} key={value} value={value}>{label}</Tab>)}</TabList>
    {(['application','storage','downloads'] as const).map(section=><div key={section} role="tabpanel" aria-labelledby={'settings-tab-'+section} hidden={tab!==section}><SettingsPage section={section}/></div>)}
    <div role="tabpanel" aria-labelledby="settings-tab-network" hidden={tab!=='network'}><Network visible={visible&&tab==='network'} anchor={anchor}/></div>
  </>;
}
