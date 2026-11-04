import {type ReactNode} from 'react';
import {Link,Tab,TabList} from '@fluentui/react-components';
import {SettingsPage,type SettingsSection} from './settings';
import {Network} from './network';

export function settingsTab(anchor:string):SettingsSection {
  const key=anchor.replace(/^\//,'').split('?')[0];
  if(['storage-settings','videoBudgetGiB'].includes(key))return 'storage';
  if(['download-settings','prepareAheadSeconds'].includes(key))return 'downloads';
  if(['network-proxy','proxy-settings'].includes(key))return 'network';
  return 'application';
}
function Settings({anchor,networkForm,networkActions,onTabSelect}:{anchor:string;networkForm:ReactNode;networkActions:ReactNode;onTabSelect:(anchor:string)=>void}) {
  const tab=settingsTab(anchor);
  return <SettingsPage section="application">{(applicationForm,applicationActions)=><SettingsPage section="storage">{(storageForm,storageActions)=><SettingsPage section="downloads">{(downloadsForm,downloadsActions)=><>
    <div className="settings-header"><TabList aria-label="设置分类" selectedValue={tab} onTabSelect={(_,v)=>onTabSelect(({application:'/settings',storage:'storage-settings',downloads:'download-settings',network:'network-proxy'} as const)[v.value as SettingsSection])}>{Object.entries({application:'常规',storage:'存储',downloads:'下载与缓存',network:'网络'}).map(([value,label])=><Tab id={'settings-tab-'+value} aria-controls={'settings-panel-'+value} key={value} value={value}>{label}</Tab>)}</TabList>
      {{application:applicationActions,storage:storageActions,downloads:downloadsActions,network:networkActions}[tab]}</div>
    {Object.entries({application:applicationForm,storage:storageForm,downloads:downloadsForm}).map(([section,form])=><div id={'settings-panel-'+section} key={section} role="tabpanel" aria-labelledby={'settings-tab-'+section} hidden={tab!==section}>{form}</div>)}
    <div id="settings-panel-network" role="tabpanel" aria-labelledby="settings-tab-network" hidden={tab!=='network'}>{networkForm}</div>
  </>}</SettingsPage>}</SettingsPage>}</SettingsPage>;
}

export function SettingsAndNetwork({settingsVisible,networkVisible,anchor,onTabSelect}:{settingsVisible:boolean;networkVisible:boolean;anchor:string;onTabSelect:(anchor:string)=>void}) {
  return <SettingsPage section="network">{(form,actions)=>{
    return <>
    <div hidden={!settingsVisible}><Settings anchor={anchor} onTabSelect={onTabSelect} networkActions={actions} networkForm={<div id="network-proxy" tabIndex={-1}>
      {form}<Link href="#network-tests">检测当前连接</Link>
    </div>}/></div>
    <div hidden={!networkVisible}><Network visible={networkVisible}/></div>
  </>;}}</SettingsPage>;
}
