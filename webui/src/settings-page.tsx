import {useState,type ReactNode} from 'react';
import {Link,Tab,TabList} from '@fluentui/react-components';
import {SettingsPage,settingsPayload,type SettingsSection} from './settings';
import {Network} from './network';

export function settingsTab(anchor:string):SettingsSection {
  const key=anchor.replace(/^\//,'').split('?')[0];
  if(['storage-settings','videoBudgetGiB'].includes(key))return 'storage';
  if(['download-settings','prepareAheadSeconds'].includes(key))return 'downloads';
  if(['network-proxy','proxy-settings'].includes(key))return 'network';
  return 'application';
}
function Settings({anchor,networkForm,onTabSelect}:{anchor:string;networkForm:ReactNode;onTabSelect:(anchor:string)=>void}) {
  const tab=settingsTab(anchor);
  return <><TabList aria-label="设置分类" selectedValue={tab} onTabSelect={(_,v)=>onTabSelect(({application:'/settings',storage:'storage-settings',downloads:'download-settings',network:'network-proxy'} as const)[v.value as SettingsSection])}>{Object.entries({application:'常规',storage:'存储',downloads:'预加载与下载',network:'网络'}).map(([value,label])=><Tab id={'settings-tab-'+value} aria-controls={'settings-panel-'+value} key={value} value={value}>{label}</Tab>)}</TabList>
    {(['application','storage','downloads'] as const).map(section=><div id={'settings-panel-'+section} key={section} role="tabpanel" aria-labelledby={'settings-tab-'+section} hidden={tab!==section}><SettingsPage section={section}/></div>)}
    <div id="settings-panel-network" role="tabpanel" aria-labelledby="settings-tab-network" hidden={tab!=='network'}>{networkForm}</div>
  </>;
}

export function SettingsAndNetwork({settingsVisible,networkVisible,anchor,onTabSelect}:{settingsVisible:boolean;networkVisible:boolean;anchor:string;onTabSelect:(anchor:string)=>void}) {
  const [upstream,setUpstream]=useState('direct');
  return <SettingsPage section="network">{(form,draft)=>{
    const {proxyBackend,socks5Address,socks5Username,socks5Password}=settingsPayload(draft);
    return <>
    <div hidden={!settingsVisible}><Settings anchor={anchor} onTabSelect={onTabSelect} networkForm={<div id="network-proxy" tabIndex={-1}>
      {form}<Link href="#network-tests" onClick={()=>setUpstream(draft.upstreamMode==='direct'?'direct':'proxy')}>检测此连接配置</Link>
    </div>}/></div>
    <div hidden={!networkVisible}><Network visible={networkVisible} proxy={{proxyBackend,socks5Address,socks5Username,socks5Password}} upstream={upstream} onUpstreamChange={setUpstream}/></div>
  </>;}}</SettingsPage>;
}
