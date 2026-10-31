import {useEffect,useRef,useState} from 'react';
import {Link,Spinner,Subtitle2,Tooltip} from '@fluentui/react-components';
import {Diagnostics} from './diagnostics';
import {NetworkMonitor} from './monitor';
import {useApp} from './hooks';
import {Feedback} from './ui';

function DashboardFrame() {
  const [loaded,setLoaded]=useState(false);
  return <div className="dashboard-frame">
    <iframe title="Mihomo 代理面板" src="/proxy/" onLoad={()=>setLoaded(true)}/>
    {!loaded&&<div className="dashboard-loading"><Spinner label="正在加载代理面板…"/></div>}
  </div>;
}

function MihomoDashboard() {
  const app=useApp(),status=app.status.data!,container=useRef<HTMLDivElement>(null);
  const [visible,setVisible]=useState(()=>!document.hidden);
  useEffect(()=>{
    let restoreFocus=false;
    const suspend=()=>{restoreFocus||=container.current?.contains(document.activeElement)??false;setVisible(false);};
    const resume=()=>{if(document.hidden)return;setVisible(true);if(restoreFocus){container.current?.focus({preventScroll:true});restoreFocus=false;}};
    const change=()=>document.hidden?suspend():resume();
    document.addEventListener('visibilitychange',change);addEventListener('pagehide',suspend);addEventListener('pageshow',resume);
    return()=>{document.removeEventListener('visibilitychange',change);removeEventListener('pagehide',suspend);removeEventListener('pageshow',resume);};
  },[]);
  const available=!app.stopped&&!app.status.error&&status.proxyNodes?.dashboard;
  const error=app.stopped?'后台程序已断开，代理面板已关闭。':app.status.error?'无法读取程序状态，请检查 SWD 是否仍在运行。':!status.proxyNodes?.dashboard?status.proxyNodes?.activeError||'代理面板不可用，请检查网络设置和日志，修复后重启程序。':'';
  return <div ref={container} className="proxy-dashboard" role="region" aria-label="代理面板" tabIndex={-1}>
    <div className="toolbar spread"><Tooltip content="面板整组测速可能并发连接组内所有节点，节点较多时可能产生连接高峰。" relationship="description"><Subtitle2 as="h2" tabIndex={0}>代理面板</Subtitle2></Tooltip>{available&&<Link href="/proxy/" target="_blank" rel="noopener noreferrer">独立窗口打开</Link>}</div>
    <Feedback error>{error}</Feedback>
    {!error&&<Feedback>{status.proxyNodes?.activeError}</Feedback>}
    {visible&&available&&<DashboardFrame/>}
  </div>;
}

export function Network({visible}:{visible:boolean}) {
  const status=useApp().status.data!;
  const direct=status.activeSettings.upstreamMode==='direct';
  const mihomo=status.activeSettings.upstreamMode==='proxy'&&status.activeSettings.proxyBackend==='mihomo';
  return <div id={!direct?'network':undefined} tabIndex={!direct?-1:undefined} className="page-stack">
    <section id="network-tests" tabIndex={-1}>{visible&&<Diagnostics visible={visible}/>}</section>
    {direct&&<NetworkMonitor/>}
    {mihomo&&visible&&<MihomoDashboard/>}
  </div>;
}
