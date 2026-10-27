import { Button, Link, Badge, Card, Title2, Table, TableHeader, TableHeaderCell, TableBody, TableRow, TableCell, Tooltip } from '@fluentui/react-components';
import { useApp, useResource } from './hooks';
import { Feedback, Panel, Progress, Hint, RecordDetails, StatusBadge } from './ui';
import { useState } from 'react';
import { CurrentDownloads } from './monitor';
import { BlockTimeline } from './block-timeline';
import { requestOutcome } from './requests';
import { active, accelerationLabel, date, dated, type RequestRecord } from './types';

const activationPermissionHint='配置游戏连接或准备退出恢复权限时，可能请求 Windows 管理员授权（UAC）。';

export function Home({visible}: {visible:boolean}) {
  const [paused,setPaused]=useState(false);
  const app = useApp(), s = app.status.data!, a = s.activation || {};
  const requests=useResource<{requests:RequestRecord[]}>('requests?limit=1',visible&&!app.stopped,app.revision);
  const recent=requests.data?.requests[0], failed=!!recent&&(recent.status>=400||['failed','incomplete','aborted'].includes(recent.outcome));
  const problem=a.error||s.cdnError||s.actionErrors?.activation||(s.hosts.conflict?s.hosts.message:'');
  const ready=s.running && s.hosts.ready;
  const uncertain=!!app.status.error||app.stopped;
  const needsExitPreparation=s.activeSettings.restoreHostsOnExit && s.hosts.managed && !s.hosts.exitRestorePrepared;
  const progress:Record<string,string>={checking:'正在检查游戏连接…',starting:'正在启动本地服务…',hosts:'正在配置游戏连接，请留意管理员授权…'};
  return <div className="page-stack">
    <Card id="activation" className="acceleration-card">
      <div className="toolbar spread"><div><Title2 as="h2">{active(s)?'正在开启加速':accelerationLabel(s)}</Title2>
        {ready&&!active(s)&&<p className="secondary">可关闭浏览器，减少资源占用</p>}
        {progress[a.phase||'']&&<p className="secondary" role="status">{progress[a.phase||'']}</p>}</div>
        <div className="inline"><Tooltip content={activationPermissionHint} relationship="description"><Button appearance="primary" size="large" disabled={app.unavailable} onClick={()=>void app.action('activation/enable')}>{active(s)?'正在处理…':ready&&!needsExitPreparation?'重新检测视频请求':'开始加速'}</Button></Tooltip><Link href="#network">检查连接</Link></div></div>
      <div className="toolbar condition-strip"><StatusBadge label="游戏连接" description={activationPermissionHint} color={uncertain?'informative':s.hosts.conflict?'danger':s.hosts.needsMigration?'warning':s.hosts.ready?'success':a.phase==='hosts'?'brand':'informative'}>{uncertain?'待确认':s.hosts.conflict?'接入冲突':s.hosts.needsMigration?'需要更新':s.hosts.ready?'已接入':a.phase==='hosts'?'接入中':'未接入'}</StatusBadge><StatusBadge label="视频服务" color={uncertain?'informative':s.cdnError?'danger':s.running?'success':a.phase==='starting'?'brand':'informative'}>{uncertain?'待确认':s.cdnError?'异常':s.running?'运行中':a.phase==='starting'?'启动中':'未运行'}</StatusBadge><Hint label="游戏接入说明">已接入表示游戏连接配置就绪；收到游戏请求和实际播放结果另行显示。</Hint><Link href="#service">查看接入诊断</Link></div>
      <div className="toolbar home-result" role="status"><span className="secondary">{requests.error?'最近请求暂时无法读取':recent?`最近请求：${requestOutcome(recent)} · HTTP ${recent.status||'未返回'} · ${date(recent.at)}`:'尚无已结束的请求'}</span><Link href="#recent">请求历史</Link>
        {(problem||failed)&&<><Badge color="danger">需要检查连接</Badge><RecordDetails title="加速错误"><p className="wrap">{problem||'最近一次请求未正常完成。'}</p><Link href="#network">检查连接</Link></RecordDetails></>}
        {needsExitPreparation&&<span>游戏接入尚未准备退出恢复，请点击开始加速。</span>}
      </div>
    </Card>
    <div className="home-columns"><CurrentDownloads visible={visible} paused={paused}/><QueuePanel/></div>
    <BlockTimeline visible={visible} paused={paused} onPausedChange={setPaused}/>
  </div>;
}
export function AccessSettings() {
  const app=useApp(), s=app.status.data!, a=s.activation || {}, received=dated(a.firstRequest), ready=s.running&&s.hosts.ready;
  return <Panel id="service" title="游戏接入" action={<Tooltip content={activationPermissionHint} relationship="description"><Button disabled={app.unavailable} onClick={()=>void app.action('activation/enable')}>{ready?'重新检测视频请求':'修复并开启加速'}</Button></Tooltip>}>
    <div className="settings-columns"><div><p>HTTP · 80：{s.running?'正在监听':s.portOK?'可用':s.portOwner?`被 ${s.portOwner.name}（PID ${s.portOwner.pid}）占用`:'不可用或被系统保留'}</p><p>HTTPS · 443：{s.running?'正在转发':s.httpsPortOK?'可用':s.httpsPortOwner?`被 ${s.httpsPortOwner.name}（PID ${s.httpsPortOwner.pid}）占用`:'不可用或被系统保留'}</p></div><div><p>游戏接入：{s.hosts.message}</p><span className="secondary">HTTP 复用本地视频；HTTPS 加密转发。</span>{dated(a.started)&&<p>检测开始：{date(a.started)}{received&&` · 首个请求：${date(a.firstRequest)}`}</p>}</div></div>
    <Feedback error>{s.cdnError||s.actionErrors?.hosts}</Feedback>
  </Panel>;

}
export function QueuePanel() {
  const app=useApp(),s=app.status.data!,q=s.queue;
  const uncertain=!!app.status.error||app.stopped,failed=!!q.desired&&!q.running&&!!s.actionErrors?.queue,attention=!!q.running&&!!(q.logError||q.error);
  const phase=uncertain?'待确认':failed?'启动失败':attention?'需检查':q.running?'监控中':q.desired?'未就绪':'已关闭';
  return <Panel id="queue" title="房间队列" action={<Link href="#prepareAheadSeconds">设置</Link>}>
    <div className="toolbar spread"><StatusBadge label="房间队列" color={uncertain?'informative':failed?'danger':attention?'warning':q.running?'success':q.desired?'warning':'informative'}>{phase}</StatusBadge><span className="secondary">提前 {s.activeSettings.prepareAheadSeconds} 秒</span></div>
    {q.running&&!uncertain&&<span className="secondary">{q.current?'正在准备歌曲':q.songs?.length?'等待队列变化':'等待房间同步'}</span>}
    <div className="queue-list"><Table size="small" aria-label="房间队列"><TableHeader><TableRow><TableHeaderCell style={{width:42}}>#</TableHeaderCell><TableHeaderCell>歌曲</TableHeaderCell><TableHeaderCell style={{width:90}}>准备状态</TableHeaderCell></TableRow></TableHeader><TableBody>{q.songs?.map((song,i)=><TableRow key={`${song.songId}:${i}`}><TableCell>{i+1}</TableCell><TableCell><Link href={'#/library?song='+song.songId}>{q.titles?.[song.songId]||song.songId}</Link></TableCell><TableCell>{q.blocked?.includes(song.songId)?'已屏蔽':(q.active||[q.current]).includes(song.songId)?'准备中':'等待'}</TableCell></TableRow>)}</TableBody></Table>{!q.songs?.length&&<p className="empty-state">暂无房间队列</p>}</div>
    <div className="toolbar spread"><span className="secondary">游戏日志：{q.logError?'读取异常':!q.running?'当前未读取':q.file?'正在读取':'尚未发现'}</span><div className="inline"><Link href="#download-settings">检查日志设置</Link><RecordDetails title="房间队列状态"><p className="wrap">{q.file||'尚未发现可读取日志'}</p><p>本次准备成功 {q.completed||0} 次</p>{(q.logError||q.error||s.actionErrors?.queue)&&<p className="wrap">{q.logError||q.error||s.actionErrors?.queue}</p>}</RecordDetails></div></div>
  </Panel>;
}
export function BatchPanel() {
  const app=useApp(), s=app.status.data!, b=s.batch, saved=s.lastBatch;
  const zeroBudget=s.activeSettings.videoStorageMode==='none';
  return <Panel id="batch" title="下载缺失视频" action={<Hint label="批量下载说明">检查整个曲库；跳过屏蔽歌曲。普通视频按存储预算补齐，收藏按保留设置处理。停止后，共享下载可能继续完成。</Hint>}>
    <div className="toolbar spread"><div className="inline"><Button disabled={app.unavailable||b.running} onClick={()=>void app.action('batch/scan')}>检查缺失视频</Button><Button appearance="primary" disabled={app.unavailable||b.running} onClick={()=>void app.action('batch/start')}>下载缺失视频</Button><Button disabled={app.unavailable||!b.running} onClick={()=>void app.action('batch/cancel')}>停止批量任务</Button></div><span role="status">{b.phase||'等待开始'}</span></div>
    {zeroBudget&&<span className="secondary">当前不保留普通视频。<Link href="#maxCacheGiB">设置保留空间</Link>{s.activeSettings.retainFavorites&&'，收藏仍可补齐。'}</span>}
    {(b.running||!!b.total)&&<><Progress value={b.checked} max={b.total} label="批量任务进度"/><span>{b.current} · 已检查 {b.checked||0}/{b.total||0} · 已有 {b.hits||0} · 缺失 {b.missing||0} · 完成 {b.downloaded||0} · 跳过 {b.skipped||0} · 失败 {b.failed||0}</span></>}
    {b.target&&<span className="secondary">本次仅处理{b.target.songID?'歌曲 ID '+b.target.songID:'资源 '+b.target.resource}</span>}
    {dated(saved.updated)&&<div className="toolbar"><span className="secondary">上次任务：{date(saved.updated)} · 缺失 {saved.missing||0} · 下载完成 {saved.downloaded||0}</span><RecordDetails title="上次完成的批量任务"><p>{saved.scanOnly?'检查本地文件':'下载缺失视频'} · 共 {saved.total||0} · 已有 {saved.hits||0} · 缺失 {saved.missing||0} · 下载完成 {saved.downloaded||0}</p>{saved.catalogWarning&&<p className="wrap">{saved.catalogWarning}</p>}</RecordDetails></div>}
    {b.catalogWarning&&<Feedback error><div className="toolbar"><span>{b.catalog?.revision?'在线曲库更新失败，本次使用已有清单。':'没有可用清单，请重试更新。'}</span><Button disabled={app.unavailable||b.running} onClick={()=>void app.action('batch/scan')}>重试更新</Button><RecordDetails title="在线更新错误详情"><p className="wrap">{b.catalogWarning}</p></RecordDetails></div></Feedback>}
    {b.budgetReached&&<Feedback>视频预算不足。<Link href="#storage-settings">调整保留空间</Link>后可重新开始。</Feedback>}
    <Feedback error>{b.error||s.actionErrors?.batch}</Feedback>
    {!!b.failed&&<Table size="small" aria-label="批量任务失败记录"><TableHeader><TableRow><TableHeaderCell>歌曲</TableHeaderCell><TableHeaderCell>原因</TableHeaderCell></TableRow></TableHeader><TableBody>{b.failures?.map(f=><TableRow key={f.id}><TableCell>{f.id} · {f.name}</TableCell><TableCell>{f.error}</TableCell></TableRow>)}</TableBody></Table>}
  </Panel>;
}
