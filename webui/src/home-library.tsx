import { Button, Link, Caption1, Table, TableHeader, TableHeaderCell, TableBody, TableRow, TableCell } from '@fluentui/react-components';
import { useApp } from './hooks';
import { Details, Feedback, Panel, Progress } from './ui';
import { active, date, dated } from './types';

export function Home() {
  const app = useApp(), s = app.status.data!, a = s.activation || {}, received = dated(a.firstRequest);
  const ready=s.running && s.hosts.ready;
  const progress:Record<string,string>={checking:'正在检查游戏连接…',starting:'正在启动本地服务…',hosts:'正在配置游戏连接，请留意管理员权限提示…'};
  const next=ready?'请在游戏中点歌确认播放。遇到问题可打开「运行状态」检查网络。':s.hosts.ready?'游戏连接已配置，开始加速后即可使用本地服务。':'点击「开始加速」，然后在游戏中点歌。';
  return <><Panel id="activation" title="游戏加速"><p>开始加速会配置游戏连接并启动本地服务。配置连接可能需要管理员权限。</p>
    <div className="actions"><Button appearance="primary" disabled={app.unavailable || ready} onClick={() => void app.action('activation/enable')}>{active(s) ? '正在处理…' : ready ? '加速运行中' : '开始加速'}</Button>
    <Link href="#service">停止服务或恢复直连</Link></div>
    <Feedback error>{a.error || s.cdnError || s.actionErrors?.activation}</Feedback>
    <ul><li>游戏连接：{s.hosts.ready?'已配置':s.hosts.conflict?'配置冲突，请查看高级接入设置':'未配置'}</li><li>本地服务：{s.running ? '运行中' : '未启动'}</li><li>房间队列预加载：{s.queue.running?'已开启':s.queue.desired?s.batch.running&&!s.batch.scanOnly?'下载补齐期间暂停':'尚未就绪，请检查游戏日志':'已关闭'}</li><li>播放验证：{received ? '已收到游戏请求，请在游戏中确认播放' : '尚未验证播放'}</li></ul>
    <Feedback error>{s.queue.logError || s.queue.error || s.actionErrors?.queue}</Feedback>
    <p role="status">{progress[a.phase || '']}</p><p>{next}</p>
    <Caption1 as="p" className="hint">队列预加载单独控制；停止播放网关或退出应用不会恢复游戏连接。</Caption1>
    </Panel><div id="service" tabIndex={-1}><Details title="高级接入设置"><Caption1 as="p" className="hint">以下操作分别控制播放网关和 hosts。停止网关或退出应用会保留连接配置。</Caption1>
    <div className="actions"><Button disabled={app.unavailable || s.running} onClick={() => void app.action('start')}>启动播放网关</Button><Button disabled={app.unavailable || !s.running} onClick={() => void app.action('stop')}>停止播放网关</Button></div>
    <Feedback error>{s.cdnError}</Feedback>
    <p>HTTP 播放 · 80：{s.running ? 'Still Wanna Dance 正在监听' : s.portOK ? '端口可用，可以启动' : s.portOwner ? `被 ${s.portOwner.name}（PID ${s.portOwner.pid}）占用，请先关闭该服务` : '端口不可用或被系统保留，请检查其他服务和端口设置'}</p>
    <p>HTTPS 转发 · 443：{s.running ? 'HTTPS 转发已就绪' : s.httpsPortOK ? '端口可用，随播放网关启动' : s.httpsPortOwner ? `被 ${s.httpsPortOwner.name}（PID ${s.httpsPortOwner.pid}）占用，请先关闭该服务` : '端口不可用或被系统保留，请检查其他服务和端口设置'}</p>
    <Caption1 as="p" className="hint">游戏 HTTP 播放可复用本地视频和视频块缓存；网页 HTTPS 播放保持加密并转发到原线路，不使用本地视频和视频块缓存。</Caption1><p>游戏接入 · hosts：{s.hosts.message}</p><Feedback error>{s.actionErrors?.hosts}</Feedback>
    <div className="actions"><Button disabled={app.unavailable} onClick={() => void app.action('hosts/enable')}>修改 hosts</Button><Button disabled={app.unavailable} onClick={() => void app.action('hosts/disable')}>恢复游戏直连</Button></div>
    <Caption1 as="p" className="hint">仅移除本程序添加的 hosts 映射，不覆盖其他程序的配置。收到请求不代表游戏已播放成功。</Caption1>
    <Button disabled={app.unavailable || !ready} onClick={() => void app.action('activation/enable')}>重新检测视频请求</Button>
    {dated(a.started) && <p>检测开始：{date(a.started)}{received&&` · 首个请求：${date(a.firstRequest)}`}</p>}
    </Details></div></>;
}
export function Library() {
  const app = useApp(), s = app.status.data!, q = s.queue, b = s.batch, saved = s.lastBatch;
  const zeroBudget = s.activeSettings.videoStorageMode === 'none';
  const queuePhase = q.running ? q.current ? `正在准备歌曲 ${(q.active || [q.current]).join('、')}` : q.songs?.length ? '等待队列变化或重试' : '等待新的队列同步' : !q.desired ? '已关闭' : b.running && !b.scanOnly ? '下载补齐期间暂停，结束后自动恢复' : '预加载未就绪，请检查日志目录';
  return <><Panel id="queue" title="自动准备房间队列"><p>根据游戏日志提前下载即将播放的内容，选择「不保留」也能使用。开启后等待房间同步新队列。</p><Link href="#prepareAheadSeconds">调整提前准备时间</Link><Details title="暂停与停止说明"><p>按预计播放时间提前准备队列中的有效曲目，不会回放旧队列。下载补齐期间暂停，结束后恢复原先开启的队列预加载。关闭后已开始的共享下载可能继续完成。</p></Details>
    <div className="actions"><Button disabled={app.unavailable || q.running || (q.desired && b.running && !b.scanOnly)} onClick={() => void app.action('queue/start')}>开启队列预加载</Button><Button disabled={app.unavailable || (!q.desired && !q.running)} onClick={() => void app.action('queue/stop')}>关闭队列预加载</Button></div>
    <p role="status">{queuePhase}</p><ol>{q.songs?.map((song,i) => <li key={`${song.songId}:${i}`}>{q.titles?.[song.songId] || song.songId}</li>)}</ol>
    <Details title="日志与累计记录"><p>{q.file || '尚未发现日志'} · 本次开启后累计准备成功 {q.completed || 0} 次</p></Details>
    <Feedback error>{q.logError || q.error || s.actionErrors?.queue}</Feedback></Panel>
    <Panel id="batch" title="保存视频供以后使用"><p>批量下载缺失视频，留待以后播放。可能占用较多流量和磁盘空间。</p>
    {zeroBudget && <Feedback>当前选择「不保留」，无法保存曲库；上方的房间队列准备仍可使用。若要保存视频，请先<Link href="#maxCacheGiB">设置视频保留空间</Link>，保存并重启应用。</Feedback>}
    <div className="actions"><Button disabled={app.unavailable || b.running} onClick={() => void app.action('batch/scan')}>{b.running&&b.scanOnly?'正在扫描…':'检查缺失视频'}</Button><Button disabled={app.unavailable || b.running || zeroBudget} onClick={() => void app.action('batch/switch')}>{b.running&&!b.scanOnly?'正在下载补齐…':q.running ? '暂停预加载并下载补齐' : '下载补齐'}</Button><Button disabled={app.unavailable || !b.running} onClick={() => void app.action('batch/cancel')}>停止批量任务</Button></div>
    <Details title="检查与下载说明"><p>检查缺失视频会更新歌曲清单并检查本地文件，不下载或校验视频内容。补齐期间暂停房间队列预加载，结束后恢复原先开启的队列预加载。空间不足时停止；停止任务后，已开始的共享下载可能继续完成。</p></Details>
    <p>{dated(saved.updated) ? `上次完成的${saved.scanOnly ? '本地检查' : '下载补齐'}${saved.catalogWarning ? '（使用已有清单）' : ''} · ${date(saved.updated)} · 任务曲目总数 ${saved.total || 0} · 检查时本地已有 ${saved.hits || 0} · 该次任务下载完成 ${saved.downloaded || 0} · 缺失视频 ${saved.missing || 0}` : '暂无完成的任务记录'}</p>
    {(b.running || !!b.total) && <Progress value={b.checked} max={b.total} label="批量任务进度" />}
    <p role="status">{b.phase || '等待开始'}</p>
    {b.catalogWarning && <Feedback error><div className="content-stack"><p>在线曲库更新失败，{b.catalog?.revision ? '本次使用已有清单。' : '本地没有可用清单，暂时无法检查视频。'}</p><Button disabled={app.unavailable || b.running} onClick={() => void app.action('batch/scan')}>重试更新</Button><Caption1 as="p">重新更新曲库并检查本地视频，不下载视频。</Caption1><Details title="在线更新错误详情"><p>{b.catalogWarning}</p></Details></div></Feedback>}<p>{b.current}{!!b.total&&` · 当前任务 ${b.checked || 0} / ${b.total} · 命中 ${b.hits || 0} · 缺失 ${b.missing || 0}${b.scanOnly?` · 本地检查失败 ${b.failed || 0}`:` · 下载完成 ${b.downloaded || 0} · 处理失败 ${b.failed || 0}`}`}</p>
    {b.budgetReached && <Feedback>剩余视频存储预算不足以容纳下一首完整视频，下载补齐已停止。<Link href="#/settings">前往设置调整视频存储预算</Link>，保存并重启应用后再次点击「下载补齐」。</Feedback>}
    <Feedback error>{b.error || s.actionErrors?.batch}</Feedback>
    {!!b.failed && <Details title="查看失败记录"><Table aria-label="批量任务失败记录"><TableHeader><TableRow><TableHeaderCell>歌曲</TableHeaderCell><TableHeaderCell>原因</TableHeaderCell></TableRow></TableHeader><TableBody>{b.failures?.map(f => <TableRow key={f.id}><TableCell>{f.id} · {f.name}</TableCell><TableCell>{f.error}</TableCell></TableRow>)}</TableBody></Table></Details>}
    </Panel></>;
}
