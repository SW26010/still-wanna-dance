import {useState} from 'react';
import {Button,Link} from '@fluentui/react-components';
import {SettingsPage,settingsPayload} from './settings';
import {Diagnostics} from './diagnostics';
import {NetworkMonitor,LegacyNetworkDetails} from './monitor';
import {AccessSettings} from './home-library';

export function Network({visible,anchor}:{visible:boolean;anchor:string}) {
  const [upstream,setUpstream]=useState('direct');
  return <SettingsPage section="network">{(form,draft)=><div className="page-stack">
    <nav className="toolbar" aria-label="网络功能"><Link href="#network-proxy">代理设置</Link><Link href="#network">当前连接</Link><Link href="#network-tests">连通性与测速</Link><Link href="#service">接入诊断</Link></nav>
    <div className="settings-columns"><div id="network-proxy" tabIndex={-1}>{form}<Button onClick={()=>{setUpstream(draft.upstreamMode==='direct'?'direct':'proxy');document.getElementById('network-tests')?.scrollIntoView();}}>检测此连接配置</Button></div><NetworkMonitor/></div>
    <section id="network-tests" tabIndex={-1}><p className="secondary">SOCKS5 检测使用当前草稿；内嵌节点需先保存并重启。</p>{visible&&<Diagnostics visible={visible} proxy={{...settingsPayload(draft),page:'proxy',upstreamMode:'proxy'}} upstream={upstream} onUpstreamChange={setUpstream}/>}</section>
    <div id="network-access" tabIndex={-1}><AccessSettings/></div><LegacyNetworkDetails/>
  </div>}</SettingsPage>;
}
