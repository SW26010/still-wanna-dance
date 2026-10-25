import {Link} from '@fluentui/react-components';
import {Diagnostics} from './diagnostics';
import {NetworkMonitor} from './monitor';
import {AccessSettings} from './home-library';

export function Network({visible,proxy,upstream,onUpstreamChange}:{visible:boolean;proxy:unknown;upstream:string;onUpstreamChange:(value:string)=>void}) {
  return <div className="page-stack">
    <nav className="toolbar" aria-label="网络功能"><Link href="#network">当前连接</Link><Link href="#network-tests">连通性与测速</Link><Link href="#service">接入诊断</Link><Link href="#network-proxy">网络设置</Link></nav>
    <NetworkMonitor/>
    <section id="network-tests" tabIndex={-1}><p className="secondary">SOCKS5 检测使用设置中的当前草稿；内嵌节点需先保存并重启。</p>{visible&&<Diagnostics visible={visible} proxy={proxy} upstream={upstream} onUpstreamChange={onUpstreamChange}/>}</section>
    <div id="network-access" tabIndex={-1}><AccessSettings/></div>
  </div>;
}
