import { Button, Link, Caption1, Table, TableHeader, TableHeaderCell, TableBody, TableRow, TableCell } from '@fluentui/react-components';
import { useApp } from './hooks';
import { Details, Feedback, Panel, Progress } from './ui';
import { active, date, dated } from './types';

export function Home() {
  const app = useApp(), s = app.status.data!, a = s.activation || {}, received = dated(a.firstRequest);
  const progress:Record<string,string>={checking:'正在检查接入配置…',starting:'正在启动播放网关并检查端口…',hosts:'播放网关已运行，正在检查 / 修改 hosts；如出现 UAC 提示，请允许管理员权限…',migration_required:s.hosts.needsMigration?'播放网关已自动启动，但 hosts 迁移未完成：'+a.error:'hosts 状态已更新，请重新检测视频请求。',failed:'启用未完成：'+a.error,stopped:s.cdnError?'播放网关已停止：'+s.cdnError:'本次检测已结束。',waiting:'启用步骤已完成，请核对下方实时状态。'};
  const next=active(s)?'请等待当前操作完成，勿重复提交。':!s.running?s.cdnError?'播放网关启动失败，请根据错误提示处理后重试。':'':s.hosts.needsMigration?'下一步：点击「启用游戏加速」清理本程序旧版 hosts 条目，完成接入迁移。':!s.hosts.ready?'下一步：检查 hosts 冲突或管理员权限后重试。播放网关已运行，接入尚未完成。':a.phase!=='waiting'?'下一步：点击「重新检测视频请求」，然后在游戏中点歌。':received?'已收到请求，请在游戏中确认播放。无法播放时，可到「运行状态」查看网络检测。':'下一步：在游戏中请求一首歌曲。如一直没有请求，请重新进入世界或重启游戏后重试。';
  return <><Panel id="activation" title="启用游戏加速" action={<Link href="#service">高级接入设置</Link>}><p>启用后，在游戏中请求一首歌曲以检查接入。首次接入可能需要管理员权限。</p>
    <p>接入会修改本机 hosts，让游戏视频使用本地缓存；网页 HTTPS 视频仅转发。退出应用不会恢复接入，停用前请在下方恢复游戏直连。</p>
    <Button appearance="primary" disabled={app.unavailable} onClick={() => void app.action('activation/enable')}>{active(s) ? '正在启用…' : s.running && s.hosts.ready ? '重新检测视频请求' : a.phase==='failed'?'重试启用游戏加速':'启用游戏加速'}</Button>
    <Feedback error>{a.error || s.cdnError}</Feedback>
    <ul><li>播放网关：{s.running ? '运行中' : '未运行'}</li><li>游戏接入：{s.hosts.ready?'已完成':s.hosts.needsMigration?s.hosts.message:'尚未完成，请点击「启用游戏加速」'}</li><li>视频请求：{received ? '本次已收到视频请求（不代表播放成功）' : a.phase === 'waiting' ? '等待本次视频请求' : a.phase==='stopped'?'检测已结束':'尚未检测'}</li></ul>
    <p role="status">{progress[a.phase || '']}</p><p>{next}</p>
    {dated(a.started) && <p>本次向导开始：{date(a.started)}{dated(a.readyAt)&&` · 检测起点：${date(a.readyAt)}`}{received&&` · 首个请求到达：${date(a.firstRequest)}`}</p>}
    <Details title="接入与检测说明"><p>游戏 HTTP 视频可使用缓存，网页 HTTPS 播放仅转发。收到请求不代表游戏已播放成功。停用或卸载前，请在「高级接入设置」中恢复游戏直连；退出应用不会自动恢复。</p></Details>
    </Panel><div id="service" tabIndex={-1}><Panel title="高级接入设置"><Caption1 as="p" className="hint">可分别控制本地播放网关和游戏接入。</Caption1>
    <div className="actions"><Button disabled={app.unavailable || s.running} onClick={() => void app.action('start')}>启动播放网关</Button><Button disabled={app.unavailable || !s.running} onClick={() => void app.action('stop')}>停止播放网关</Button></div>
    <Feedback error>{s.cdnError}</Feedback>
    <p>HTTP 播放 · 80：{s.running ? 'Still Wanna Dance 正在监听' : s.portOK ? '端口可用，可以启动' : s.portOwner ? `被 ${s.portOwner.name}（PID ${s.portOwner.pid}）占用，请先关闭该服务` : '端口不可用或被系统保留，请检查其他服务和端口设置'}</p>
    <p>HTTPS 转发 · 443：{s.running ? 'HTTPS 转发已就绪' : s.httpsPortOK ? '端口可用，随播放网关启动' : s.httpsPortOwner ? `被 ${s.httpsPortOwner.name}（PID ${s.httpsPortOwner.pid}）占用，请先关闭该服务` : '端口不可用或被系统保留，请检查其他服务和端口设置'}</p>
    <Caption1 as="p" className="hint">游戏 HTTP 播放使用本地缓存；网页 HTTPS 播放保持加密并转发到原线路，不使用本地缓存。</Caption1><p>游戏接入 · hosts：{s.hosts.message}</p><Feedback error>{s.actionErrors?.hosts}</Feedback>
    <div className="actions"><Button disabled={app.unavailable} onClick={() => void app.action('hosts/enable')}>修改 hosts</Button><Button disabled={app.unavailable} onClick={() => void app.action('hosts/disable')}>恢复游戏直连</Button></div>
    <Caption1 as="p" className="hint">仅在实际修改时请求管理员权限。映射长期保留，停止播放网关或退出控制台不会恢复。「恢复游戏直连」仅移除 Still Wanna Dance 添加的映射；如有其他映射，仍需根据接入状态检查。</Caption1>
    </Panel></div></>;
}
export function Library() {
  const app = useApp(), s = app.status.data!, q = s.queue, b = s.batch, saved = s.lastBatch;
  const zeroBudget = !s.activeSettings.cacheUnlimited && s.activeSettings.maxCacheBytes === 0;
  const queuePhase = q.running ? q.current ? `正在准备歌曲 ${(q.active || [q.current]).join('、')}` : q.songs?.length ? '等待队列变化或重试' : '等待新的队列同步' : !q.desired ? '已关闭' : b.running && !b.scanOnly ? '下载补齐期间暂停，结束后自动恢复' : '预缓存未就绪，请检查日志目录';
  return <><Panel id="queue" title="房间队列预缓存"><p>按预计播放时间提前准备队列中的有效曲目。</p><p>日常跳舞建议开启：根据游戏日志，提前下载即将播放的视频。开启后等待房间同步新队列，不会回放旧队列。</p><Link href="#prepareAheadSeconds">调整提前准备时间</Link><Details title="暂停与停止说明"><p>下载补齐期间暂停，结束后恢复原先开启的队列预缓存。关闭后已开始的共享下载可能继续完成。</p></Details>
    <div className="actions"><Button disabled={app.unavailable || q.running || (q.desired && b.running && !b.scanOnly)} onClick={() => void app.action('queue/start')}>开启队列预缓存</Button><Button disabled={app.unavailable || (!q.desired && !q.running)} onClick={() => void app.action('queue/stop')}>关闭队列预缓存</Button></div>
    <p role="status">{queuePhase}</p><ol>{q.songs?.map((song,i) => <li key={`${song.songId}:${i}`}>{q.titles?.[song.songId] || song.songId}</li>)}</ol>
    <Details title="日志与累计记录"><p>{q.file || '尚未发现日志'} · 本次开启后累计准备成功 {q.completed || 0} 次</p></Details>
    <Feedback error>{q.logError || q.error || s.actionErrors?.queue}</Feedback></Panel>
    <Panel id="batch" title="全曲库补齐"><p>适合想提前准备整个曲库时使用。补齐可能下载大量视频、占用较多流量和磁盘空间，建议先<Link href="#maxCacheGiB">设置缓存上限</Link>。</p>
    <div className="actions"><Button disabled={app.unavailable || b.running} onClick={() => void app.action('batch/scan')}>{b.running&&b.scanOnly?'正在扫描…':'检查缺失视频'}</Button><Button disabled={app.unavailable || b.running || zeroBudget} onClick={() => void app.action('batch/switch')}>{b.running&&!b.scanOnly?'正在下载补齐…':q.running ? '暂停预缓存并下载补齐' : '下载补齐'}</Button><Button disabled={app.unavailable || !b.running} onClick={() => void app.action('batch/cancel')}>停止批量任务</Button></div>
    {zeroBudget && <Feedback>当前缓存上限为 0 GiB，不持久保留缓存，无法下载补齐。请在设置中选择正数容量或不限制，保存并重启应用后重试。</Feedback>}
    <Details title="检查与下载说明"><p>检查缺失视频会更新歌曲清单并检查本地文件，不下载或校验视频内容。补齐期间暂停房间队列预缓存，结束后恢复原先开启的队列预缓存。空间不足时停止；停止任务后，已开始的共享下载可能继续完成。</p></Details>
    <p>{dated(saved.updated) ? `上次成功${saved.scanOnly ? '扫描' : '下载补齐'} · ${date(saved.updated)} · 任务曲目总数 ${saved.total || 0} · 检查时已缓存 ${saved.hits || 0} · 该次任务下载完成 ${saved.downloaded || 0} · 缺失视频 ${saved.missing || 0}` : '暂无成功任务记录'}</p>
    {(b.running || !!b.total) && <Progress value={b.checked} max={b.total} label="批量任务进度" />}
    <p role="status">{b.phase || '等待开始'} {b.catalogWarning}</p><p>{b.current}{!!b.total&&` · 当前任务 ${b.checked || 0} / ${b.total} · 命中 ${b.hits || 0} · 缺失 ${b.missing || 0}${b.scanOnly?` · 本地文件命中 ${b.reused || 0}`:''} · 下载完成 ${b.downloaded || 0} · 失败 ${b.failed || 0}`}</p>
    {b.budgetReached && <Feedback>剩余容量不足以容纳下一首完整视频，下载补齐已停止。<Link href="#/settings">前往设置调整视频缓存上限</Link>，保存并重启应用后再次点击「下载补齐」。</Feedback>}
    <Feedback error>{b.error || s.actionErrors?.batch}</Feedback>
    {!!b.failed && <Details title="查看失败记录"><Table aria-label="批量任务失败记录"><TableHeader><TableRow><TableHeaderCell>歌曲</TableHeaderCell><TableHeaderCell>原因</TableHeaderCell></TableRow></TableHeader><TableBody>{b.failures?.map(f => <TableRow key={f.id}><TableCell>{f.id} · {f.name}</TableCell><TableCell>{f.error}</TableCell></TableRow>)}</TableBody></Table></Details>}
    </Panel></>;
}
