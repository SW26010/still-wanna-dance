import {useLayoutEffect,useState} from 'react';
import {Tab,TabList,Button,Link} from '@fluentui/react-components';
import {SettingsPage,settingsPayload} from './settings';
import {Diagnostics} from './diagnostics';
import {NetworkMonitor,LegacyNetworkDetails} from './monitor';
import {AccessSettings} from './home-library';
import {Details} from './ui';
import {useApp} from './hooks';

type NetworkTab='check'|'proxy'|'access';
export function Network({visible,anchor}:{visible:boolean;anchor:string}) {
  const app=useApp(), [tab,setTab]=useState<NetworkTab>('check'), [upstream,setUpstream]=useState('direct');
  useLayoutEffect(()=>{
    if(!visible)return;
    const next=anchor==='network-proxy'?'proxy':anchor==='network-access'||anchor==='service'?'access':'check';
    setTab(next);
    if(['network-proxy','network-access','service'].includes(anchor))requestAnimationFrame(()=>{
      const target=document.getElementById(anchor);target?.focus();target?.scrollIntoView();
    });
  },[visible,anchor]);
  return <SettingsPage section="network">{(form,draft)=><>
    <TabList selectedValue={tab} onTabSelect={(_,data)=>setTab(data.value as NetworkTab)} aria-label="网络功能">
      <Tab id="tab-check" value="check">连接检测</Tab><Tab id="tab-proxy" value="proxy">代理设置</Tab><Tab id="tab-access" value="access">高级接入</Tab>
    </TabList>
    <div role="tabpanel" aria-labelledby="tab-check" hidden={tab!=='check'}>
      <NetworkMonitor/>
      <p>修改代理后，可在此检测外部 SOCKS5 草稿。节点配置需保存并重启，再检查当前连接。</p>
        {visible&&tab==='check'&&<Diagnostics visible={visible&&tab==='check'} proxy={{...settingsPayload(draft),page:'proxy',upstreamMode:'proxy'}} upstream={upstream} onUpstreamChange={setUpstream}/>}
      <Link href="#recent">查看请求时间线</Link><Details title="线路诊断"><LegacyNetworkDetails/></Details>
    </div>
    <div id="network-proxy" tabIndex={-1} role="tabpanel" aria-labelledby="tab-proxy" hidden={tab!=='proxy'}>
      {form}<Button onClick={()=>{setUpstream(draft.upstreamMode==='direct'?'direct':'proxy');setTab('check');}}>检测此连接配置</Button>
      {app.status.data!.restartRequired&&<p>当前连接仍使用重启前的配置；保存的更改会在重启后生效。</p>}
    </div>
    <div id="network-access" tabIndex={-1} role="tabpanel" aria-labelledby="tab-access" hidden={tab!=='access'}><AccessSettings/></div>
  </>}</SettingsPage>;
}
