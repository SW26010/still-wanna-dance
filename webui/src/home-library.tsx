import { Button, Link, Caption1, Checkbox, Table, TableHeader, TableHeaderCell, TableBody, TableRow, TableCell } from '@fluentui/react-components';
import { useApp, useResource } from './hooks';
import { Details, Feedback, Panel, Progress } from './ui';
import { useState } from 'react';
import { CurrentDownloads } from './monitor';
import { BlockTimeline } from './block-timeline';
import { requestOutcome } from './requests';
import { active, exitConnectionMessage, date, dated, type RequestRecord } from './types';

export function Home({visible}: {visible:boolean}) {
  const app = useApp(), s = app.status.data!, a = s.activation || {}, received = dated(a.firstRequest);
  const requests=useResource<{requests:RequestRecord[]}>('requests?limit=1',visible&&!app.stopped,app.revision);
  const recent=requests.data?.requests[0], failed=!!recent&&(recent.status>=400||['failed','incomplete','aborted'].includes(recent.outcome));
  const problem=a.error||s.cdnError||s.actionErrors?.activation||(s.hosts.conflict?s.hosts.message:'');
  const ready=s.running && s.hosts.ready;
  const needsExitPreparation=s.activeSettings.restoreHostsOnExit && s.hosts.managed && !s.hosts.exitRestorePrepared;
  const requestStatus=a.phase==='waiting'
    ? received?'已收到游戏请求，请在游戏中确认播放':'检测已开始，尚未收到游戏请求；请在游戏中点歌。'
    : active(s)?'正在准备请求检测…'
    : a.phase==='stopped'?'请求检测已停止'
    : a.phase==='failed'||a.phase==='migration_required'?'请求检测未能开始，请先处理连接问题。'
    : '尚未检测游戏请求';
  const progress:Record<string,string>={checking:'正在检查游戏连接…',starting:'正在启动本地服务…',hosts:'正在配置游戏连接，请留意管理员权限提示…'};
  const next=ready?'请在游戏中点歌确认播放。遇到问题可打开「网络与代理」检查网络。':s.hosts.ready?'游戏连接已配置，开始加速后即可使用本地服务。':'点击「开始加速」，然后在游戏中点歌。';
  return <><Panel id="activation" title="加速状态"><p>开始加速会配置游戏连接并启动本地服务。配置连接可能需要管理员权限。</p>
    <div className="actions"><Button appearance="primary" disabled={app.unavailable || (ready && !needsExitPreparation)} onClick={() => void app.action('activation/enable')}>{active(s) ? '正在处理…' : ready && !needsExitPreparation ? '加速运行中' : '开始加速'}</Button>
    <Link href="#service">查看接入诊断</Link></div>

    <ul><li>游戏连接：{s.hosts.ready?'已配置':s.hosts.conflict?'配置冲突，请查看高级接入设置':'未配置'}</li><li>本地服务：{s.running ? '运行中' : '未启动'}</li></ul>

    <p role="status">{progress[a.phase || '']}</p><p>{next}</p>
    <Caption1 as="p" className="hint">队列按设置随网关启用。{exitConnectionMessage(s)}</Caption1>
    </Panel><Panel title="游戏请求"><p>{requestStatus}</p><p>{requests.error?'最近请求暂时无法读取，将自动重试。':recent?`最近一次已结束请求：${requestOutcome(recent)} · HTTP ${recent.status || '未返回'} · ${date(recent.at)}`:'尚无已结束的请求记录'}</p><Caption1 as="p">请求结果只表示本地服务的处理情况，音画仍需在游戏中确认。</Caption1><div className="actions"><Link href="#service">重新检测视频请求</Link><Link href="#recent">查看请求详情</Link></div></Panel>
    {(problem||failed)&&<Feedback error><div className="content-stack"><p>{problem || '最近一次请求未正常完成，请检查连接与请求详情。'}</p><Link href="#/network">检查连接</Link></div></Feedback>}
    <CurrentDownloads visible={visible}/><QueuePanel/><DownloadDiagnostics visible={visible}/></>;

}
export function AccessSettings() {
  const app=useApp(), s=app.status.data!, a=s.activation || {}, received=dated(a.firstRequest), ready=s.running&&s.hosts.ready;
  return <><div id="service" tabIndex={-1}><Panel title="高级接入"><Caption1 as="p" className="hint">接入诊断复用一键加速流程。{exitConnectionMessage(s)}</Caption1>
    <Feedback error>{s.cdnError}</Feedback>
    <p>HTTP 播放 · 80：{s.running ? 'Still Wanna Dance 正在监听' : s.portOK ? '端口可用，可以启动' : s.portOwner ? `被 ${s.portOwner.name}（PID ${s.portOwner.pid}）占用，请先关闭该服务` : '端口不可用或被系统保留，请检查其他服务和端口设置'}</p>
    <p>HTTPS 转发 · 443：{s.running ? 'HTTPS 转发已就绪' : s.httpsPortOK ? '端口可用，随播放网关启动' : s.httpsPortOwner ? `被 ${s.httpsPortOwner.name}（PID ${s.httpsPortOwner.pid}）占用，请先关闭该服务` : '端口不可用或被系统保留，请检查其他服务和端口设置'}</p>
    <Caption1 as="p" className="hint">游戏 HTTP 播放可复用本地视频和视频块缓存；网页 HTTPS 播放保持加密并转发到原线路，不使用本地视频和视频块缓存。</Caption1><p>游戏接入 · hosts：{s.hosts.message}</p><Feedback error>{s.actionErrors?.hosts}</Feedback>
    <Caption1 as="p" className="hint">冲突不会覆盖其他程序的映射。收到请求不代表游戏已播放成功。</Caption1>
    <Button disabled={app.unavailable} onClick={() => void app.action('activation/enable')}>{ready?'重新检测视频请求':'修复并开启加速'}</Button>
    {dated(a.started) && <p>检测开始：{date(a.started)}{received&&` · 首个请求：${date(a.firstRequest)}`}</p>}
    </Panel></div></>;
}
export function DownloadDiagnostics({visible}:{visible:boolean}) {
  const [paused,setPaused]=useState(false),[open,setOpen]=useState(false);
  return <Details title="下载诊断" keepMounted expanded={open} onToggle={setOpen}><Checkbox label="暂停下载展示" checked={paused} onChange={(_,v)=>setPaused(!!v.checked)}/><p>暂停仅冻结展示，后台任务继续运行。</p><BlockTimeline visible={visible&&open} paused={paused}/></Details>;
}
export function QueuePanel() {
  const app = useApp(), s = app.status.data!, q = s.queue;
  const queuePhase = q.running ? q.current ? `正在准备歌曲 ${(q.active || [q.current]).join('、')}` : q.songs?.length ? '等待队列变化或重试' : '等待新的队列同步' : !q.desired ? '已关闭' : '预加载未就绪，请检查日志目录';
  return <><Panel id="queue" title="自动准备房间队列"><p>根据游戏日志提前下载即将播放的内容，选择「不保留」也能使用。开启后等待房间同步新队列。</p><p>提前准备时间：{s.activeSettings.prepareAheadSeconds} 秒 · <Link href="#prepareAheadSeconds">调整提前准备时间</Link></p><Details title="预加载说明"><p>按预计播放时间提前准备队列中的有效曲目，不会回放旧队列。手动下载与队列预加载共同调度，互不暂停。网关停止时撤销队列需求，其他来源的共享下载仍可继续。</p></Details>
    <p role="status">{queuePhase}</p><ol>{q.songs?.map((song,i) => <li key={`${song.songId}:${i}`}>{q.titles?.[song.songId] || song.songId}{q.blocked?.includes(song.songId)&&' · 已屏蔽，跳过新的准备'}</li>)}</ol>
    <Details title="日志与累计记录"><p>{q.file || '尚未发现日志'} · 本次开启后累计准备成功 {q.completed || 0} 次</p></Details>
    <Feedback error>{q.logError || q.error || s.actionErrors?.queue}</Feedback></Panel>
    </>;
}
export function BatchPanel() {
  const app=useApp(), s=app.status.data!, b=s.batch, saved=s.lastBatch;
  const zeroBudget=s.activeSettings.videoStorageMode==='none';
  return <>
    <Panel id="batch" title="下载缺失视频"><p>检查范围为整个曲库；下载缺失视频，留待以后播放。可能占用较多流量和磁盘空间。</p>
    {zeroBudget && <Feedback>当前选择「不保留」，{s.activeSettings.retainFavorites?'下载补齐仍可处理收藏歌曲；普通歌曲需要先':'全部歌曲均按普通预算处理，需要先'}<Link href="#maxCacheGiB">设置视频保留空间</Link>。收藏仅做标记，可在<Link href="#song-preferences">歌曲偏好</Link>中管理。</Feedback>}
    <div className="actions"><Button disabled={app.unavailable || b.running} onClick={() => void app.action('batch/scan')}>{b.running&&b.scanOnly?'正在扫描…':'检查缺失视频'}</Button><Button disabled={app.unavailable || b.running} onClick={() => void app.action('batch/start')}>{b.running&&!b.scanOnly?'正在下载…':'下载缺失视频'}</Button><Button disabled={app.unavailable || !b.running} onClick={() => void app.action('batch/cancel')}>停止批量任务</Button></div>
    <Details title="检查与下载说明"><p>检查缺失视频会更新歌曲清单并检查本地文件，不下载或校验视频内容。补齐与房间队列预加载共同调度，互不暂停。视频空间不足时停止普通补齐；开启「优先保留收藏视频」时，收藏继续处理。屏蔽歌曲跳过。停止任务后，已开始的共享下载可能继续完成。</p></Details>
    <Details title="上次完成的批量任务"><p>{dated(saved.updated) ? `上次完成的${saved.scanOnly ? '本地检查' : '下载缺失视频'}${saved.catalogWarning ? '（使用已有清单）' : ''} · ${date(saved.updated)} · 任务曲目总数 ${saved.total || 0} · 检查时本地已有 ${saved.hits || 0} · 该次任务下载完成 ${saved.downloaded || 0} · 缺失视频 ${saved.missing || 0}` : '暂无完成的任务记录'}</p></Details>
    {(b.running || !!b.total) && <Progress value={b.checked} max={b.total} label="批量任务进度" />}
    <p>{b.target?`本次仅处理${b.target.songID?"歌曲 ID "+b.target.songID:"资源 "+b.target.resource}`:""}</p><p role="status">{b.phase || '等待开始'}</p>
    {b.catalogWarning && <Feedback error><div className="content-stack"><p>在线曲库更新失败，{b.catalog?.revision ? '本次使用已有清单。' : '本地没有可用清单，暂时无法检查视频。'}</p><Button disabled={app.unavailable || b.running} onClick={() => void app.action('batch/scan')}>重试更新</Button><Caption1 as="p">重新更新曲库并检查本地视频，不下载视频。</Caption1><Details title="在线更新错误详情"><p>{b.catalogWarning}</p></Details></div></Feedback>}<p>{b.current}{!!b.total&&` · 当前任务 ${b.checked || 0} / ${b.total} · 命中 ${b.hits || 0} · 缺失 ${b.missing || 0}${b.scanOnly?` · 本地检查失败 ${b.failed || 0}`:` · 下载完成 ${b.downloaded || 0} · 屏蔽跳过 ${b.skipped || 0} · 处理失败 ${b.failed || 0}`}`}</p>
    {b.budgetReached && <Feedback>视频预算不足。{s.activeSettings.retainFavorites&&'本轮可处理的收藏已继续处理。'}<Link href="#storage-settings">调整视频保留空间</Link>，保存并重启应用后再次点击「下载缺失视频」。</Feedback>}
    <Feedback error>{b.error || s.actionErrors?.batch}</Feedback>
    {!!b.failed && <Details title="查看失败记录"><Table aria-label="批量任务失败记录"><TableHeader><TableRow><TableHeaderCell>歌曲</TableHeaderCell><TableHeaderCell>原因</TableHeaderCell></TableRow></TableHeader><TableBody>{b.failures?.map(f => <TableRow key={f.id}><TableCell>{f.id} · {f.name}</TableCell><TableCell>{f.error}</TableCell></TableRow>)}</TableBody></Table></Details>}
    </Panel></>;
}
