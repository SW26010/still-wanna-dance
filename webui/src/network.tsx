import {useEffect,useRef,useState} from 'react';
import {Button,Caption1,Link,Spinner,Subtitle2,Text,Tooltip} from '@fluentui/react-components';
import {ArrowSync20Regular} from '@fluentui/react-icons';
import {request} from './api';
import {Diagnostics} from './diagnostics';
import {NetworkMonitor} from './monitor';
import {useApp} from './hooks';
import {Feedback,Notification,Panel,StatusBadge} from './ui';
import {date,dated} from './types';

function SOCKS5Service() {
  const app=useApp(),service=app.status.data?.socks5Service;
  const [pending,setPending]=useState(false),[waiting,setWaiting]=useState<{observedAt?:string}>(),[error,setError]=useState('');
  useEffect(()=>{if(waiting&&service?.observedAt!==waiting.observedAt)setWaiting(undefined);},[service?.observedAt,waiting]);
  const busy=pending||!!waiting||!!service?.checking;
  async function check() {
    if(busy||app.unavailable)return;
    setPending(true);setWaiting({observedAt:service?.observedAt});setError('');
    try {await request('network/socks5/check',{});}
    catch(e) {setWaiting(undefined);setError('检测未确认：'+String(e));}
    finally {setPending(false);app.status.refresh();}
  }
  const unknown=app.stopped||!!app.status.error||!!service?.closed;
  const state=unknown?'待确认':busy?'检测中':service?.state==='available'?'可用':service?.state==='failed'?'检查失败':'尚未检测';
  const description=unknown?'无法确认当前服务状态':busy?'正在协商与认证':service?.error|| (service?.state==='available'?'':'等待自动检查');
  return <Panel title="SOCKS5 服务" action={<Tooltip content="重新检测 SOCKS5 服务" relationship="label"><Button size="small" appearance="subtle" icon={<ArrowSync20Regular className="refresh-icon" aria-hidden="true"/>} aria-busy={busy} disabled={app.unavailable||unknown||busy} disabledFocusable={busy} onClick={()=>void check()}/></Tooltip>}>
    <Text className="wrap">{app.status.data?.activeSettings.socks5Address}</Text>
    <div className="toolbar"><StatusBadge label="服务" color={unknown?'informative':busy?'brand':service?.state==='available'?'success':service?.state==='failed'?'danger':'informative'} description={description}>{state}</StatusBadge>
      {!unknown&&!busy&&service?.latencyMS!=null&&<Text>{Math.round(service.latencyMS)} ms</Text>}
    </div>
    <Feedback error>{!unknown&&!busy?service?.error:undefined}</Feedback>
    <div className="toolbar secondary"><Caption1>最后检查：{dated(service?.observedAt)?<time dateTime={service?.observedAt}>{date(service?.observedAt)}</time>:'—'}</Caption1>
      {!unknown&&dated(service?.nextCheck)&&<Caption1>下次检查：<time dateTime={service?.nextCheck}>{date(service?.nextCheck)}</time></Caption1>}
    </div>
    <Notification message={error} error/>
  </Panel>;
}

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
    <div className="toolbar spread"><Subtitle2 as="h2">代理面板</Subtitle2>{available&&<Link href="/proxy/" target="_blank" rel="noopener noreferrer">独立窗口打开</Link>}</div>
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
    <section id="network-tests" className="content-stack" tabIndex={-1}>{visible&&<Diagnostics visible={visible}/>}</section>
    {!direct&&!mihomo&&<SOCKS5Service/>}
    {direct&&<NetworkMonitor/>}
    {mihomo&&visible&&<MihomoDashboard/>}
  </div>;
}
