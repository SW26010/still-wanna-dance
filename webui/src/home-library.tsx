import { Button, Link, Card, Title2, Table, TableHeader, TableHeaderCell, TableBody, TableRow, TableCell, Tooltip } from '@fluentui/react-components';
import { useApp, useResource } from './hooks';
import { Feedback, Panel, Progress, Hint, RecordDetails, StatusBadge } from './ui';
import { BlockTimeline } from './block-timeline';
import { StorageBudgetNotice } from './cache-capacity';
import { requestOutcome } from './requests';
import { active, accelerationLabel, date, dated, type RequestRecord, type Status } from './types';

const activationPermissionHint='需要配置游戏连接时，可能请求 Windows 管理员授权（UAC）。';
const portState=(available:boolean,owner?:Status['portOwner'])=>available?'可用':owner?`被 ${owner.name}（PID ${owner.pid}）占用`:'不可用或被系统保留';

export function Home({visible}: {visible:boolean}) {
  const app = useApp(), s = app.status.data!, a = s.activation || {}, q = s.queue;
  const requests=useResource<{requests:RequestRecord[]}>('requests?limit=1',visible&&!app.stopped,app.revision);
  const recent=requests.data?.requests[0];
  const uncertain=!!app.status.error||app.stopped;
  const unknownReason='当前无法确认程序状态，请等待后台连接恢复。';
  const needsExitPreparation=s.activeSettings.restoreHostsOnExit && s.hosts.managed && !s.hosts.exitRestorePrepared;
  const connectionError=a.connectionError||(s.hosts.conflict?s.hosts.message:'');
  const connectionReason=connectionError||(needsExitPreparation?'尚未取得恢复权限，停止或退出时无法恢复直连。':'');
  const connectionPhase=uncertain?'待确认':s.hosts.conflict?'接入冲突':connectionError?(s.hosts.ready?'需检查':'接入失败'):needsExitPreparation?'需检查':s.hosts.ready?'已接入':a.phase==='hosts'?'接入中':'未接入';
  const portError=s.running?'':[!s.portOK&&`HTTP 80 端口${portState(s.portOK,s.portOwner)}`,!s.httpsPortOK&&`HTTPS 443 端口${portState(s.httpsPortOK,s.httpsPortOwner)}`].filter(Boolean).join('；');
  const serviceError=s.cdnError||portError;
  const queueFailed=!!q.desired&&!q.running&&!!s.actionErrors?.queue;
  const queueError=q.running?[q.logError,q.error].filter(Boolean).join('；'):queueFailed?s.actionErrors?.queue:'';
  const queueReason=queueError||(q.running?(q.file?`正在监控日志：${q.file}。${q.songs?.length?`当前房间队列 ${q.songs.length} 首。`:'等待新的房间队列同步。'}`:'日志目录可读取，尚未发现 VRChat 日志，等待游戏生成日志。'):q.desired?'房间队列已启用，监控尚未就绪。':'房间队列监控未运行。');
  const queuePhase=uncertain?'待确认':queueFailed?'启动失败':queueError?'需检查':q.running?'监控中':q.desired?'未就绪':'已关闭';
  const stopping=a.phase==='stopping';
  const stopHint=s.activeSettings.restoreHostsOnExit?needsExitPreparation?'停止视频服务及队列预加载；尚未取得恢复权限，将保留游戏接入。':'停止视频服务及队列预加载，并恢复本程序的游戏接入；不再请求管理员权限。':'停止视频服务及队列预加载，保留游戏接入。继续播放前请点击「开始加速」。';
  return <div className="page-stack">
    <Card id="activation" size="small" className="acceleration-card">
      <div className="toolbar spread acceleration-heading"><div className="acceleration-title"><Title2 as="h2" aria-live="polite" aria-atomic="true">{stopping?'正在停止加速':active(s)?'正在开启加速':accelerationLabel(s)}</Title2>
        {s.running&&s.hosts.ready&&!connectionReason&&!serviceError&&!active(s)&&!uncertain&&<span className="secondary acceleration-hint">可关闭浏览器，减少资源占用</span>}</div>
        <Tooltip content={s.running?stopHint:activationPermissionHint} relationship="description"><Button appearance="primary" size="large" disabled={app.unavailable} onClick={()=>void app.action(s.running?'activation/disable':'activation/enable')}>{s.running?'停止加速':'开始加速'}</Button></Tooltip></div>
      <div className="toolbar condition-strip">
        <StatusBadge label="游戏连接" description={uncertain?unknownReason:connectionReason||activationPermissionHint} action={!uncertain&&s.hosts.ready&&needsExitPreparation?{label:'修复',onClick:()=>void app.action('hosts/prepare'),disabled:app.unavailable}:undefined} color={uncertain?'informative':s.hosts.conflict||connectionError&&!s.hosts.ready?'danger':connectionReason?'warning':s.hosts.ready?'success':a.phase==='hosts'?'brand':'informative'}>{connectionPhase}</StatusBadge>
        <StatusBadge label="视频服务" description={uncertain?unknownReason:serviceError||undefined} color={uncertain?'informative':serviceError?'danger':s.running?'success':a.phase==='starting'?'brand':'informative'}>{uncertain?'待确认':serviceError?'异常':s.running?'运行中':a.phase==='starting'?'启动中':'未运行'}</StatusBadge>
        <StatusBadge label="房间队列" description={uncertain?unknownReason:queueReason} color={uncertain?'informative':queueFailed?'danger':queueError?'warning':q.running?'success':q.desired?'warning':'informative'}>{queuePhase}</StatusBadge>
      </div>
      <div className="toolbar home-result" role="status"><span className="secondary"><Link href="#recent">最近请求</Link>：{requests.error?'暂时无法读取':recent?`${requestOutcome(recent)} · HTTP ${recent.status||'未返回'} · ${date(recent.at)}`:'暂无记录'}</span>
      </div>
    </Card>
    <BlockTimeline visible={visible}/>
  </div>;
}
export function AccessSettings() {
  const app=useApp(), s=app.status.data!, a=s.activation || {}, received=dated(a.firstRequest), ready=s.running&&s.hosts.ready;
  return <Panel id="service" title="游戏接入" action={<Tooltip content={activationPermissionHint} relationship="description"><Button disabled={app.unavailable} onClick={()=>void app.action('activation/enable')}>{ready?'重新检测视频请求':'修复并开启加速'}</Button></Tooltip>}>
    <div className="settings-columns"><div><p>HTTP · 80：{s.running?'正在监听':portState(s.portOK,s.portOwner)}</p><p>HTTPS · 443：{s.running?'正在转发':portState(s.httpsPortOK,s.httpsPortOwner)}</p></div><div><p>游戏接入：{s.hosts.message}</p><span className="secondary">HTTP 复用本地视频；HTTPS 加密转发。</span>{dated(a.started)&&<p>检测开始：{date(a.started)}{received&&` · 首个请求：${date(a.firstRequest)}`}</p>}</div></div>
    <Feedback error>{s.cdnError||s.actionErrors?.activation}</Feedback>
  </Panel>;

}
export function BatchPanel() {
  const app=useApp(), s=app.status.data!, b=s.batch, saved=s.lastBatch;
  return <Panel id="batch" title="下载缺失视频" action={<Hint label="批量下载说明">检查整个曲库；跳过屏蔽歌曲。普通视频按存储预算补齐，收藏按保留设置处理。停止后，共享下载可能继续完成。</Hint>}>
    <StorageBudgetNotice settings={s.activeSettings}/>
    <div className="toolbar spread"><div className="inline"><Button disabled={app.unavailable||b.running} onClick={()=>void app.action('batch/scan')}>检查缺失视频</Button><Button appearance="primary" disabled={app.unavailable||b.running} onClick={()=>void app.action('batch/start')}>下载缺失视频</Button><Button disabled={app.unavailable||!b.running} onClick={()=>void app.action('batch/cancel')}>停止批量任务</Button></div><span role="status">{b.phase||'等待开始'}</span></div>
    {(b.running||!!b.total)&&<><Progress value={b.checked} max={b.total} label="批量任务进度"/><span>{b.current} · 已检查 {b.checked||0}/{b.total||0} · 已有 {b.hits||0} · 缺失 {b.missing||0} · 完成 {b.downloaded||0} · 跳过 {b.skipped||0} · 失败 {b.failed||0}</span></>}
    {b.target&&<span className="secondary">本次仅处理{b.target.songID?'歌曲 ID '+b.target.songID:'资源 '+b.target.resource}</span>}
    {dated(saved.updated)&&<div className="toolbar"><span className="secondary">上次任务：{date(saved.updated)} · 缺失 {saved.missing||0} · 下载完成 {saved.downloaded||0}</span><RecordDetails title="上次完成的批量任务"><p>{saved.scanOnly?'检查本地文件':'下载缺失视频'} · 共 {saved.total||0} · 已有 {saved.hits||0} · 缺失 {saved.missing||0} · 下载完成 {saved.downloaded||0}</p>{saved.catalogWarning&&<p className="wrap">{saved.catalogWarning}</p>}</RecordDetails></div>}
    {b.catalogWarning&&<Feedback error><div className="toolbar"><span>{b.catalog?.revision?'在线曲库更新失败，本次使用已有清单。':'没有可用清单，请重试更新。'}</span><Button disabled={app.unavailable||b.running} onClick={()=>void app.action('batch/scan')}>重试更新</Button><RecordDetails title="在线更新错误详情"><p className="wrap">{b.catalogWarning}</p></RecordDetails></div></Feedback>}
    {b.budgetReached&&<Feedback>视频预算不足。<Link href="#storage-settings">调整保留空间</Link>后可重新开始。</Feedback>}
    <Feedback error>{b.error||s.actionErrors?.batch}</Feedback>
    {!!b.failed&&<Table size="small" aria-label="批量任务失败记录"><TableHeader><TableRow><TableHeaderCell>歌曲</TableHeaderCell><TableHeaderCell>原因</TableHeaderCell></TableRow></TableHeader><TableBody>{b.failures?.map(f=><TableRow key={f.id}><TableCell>{f.id} · {f.name}</TableCell><TableCell>{f.error}</TableCell></TableRow>)}</TableBody></Table>}
  </Panel>;
}
