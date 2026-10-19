import { useState } from 'react';
import { Button, Link, Title2, Card } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';
import { APIError, request, hostsActionTimeout, hostsActionPending, unconfirmedAction } from './api';
import { DocumentLayout, Feedback, Panel } from './ui';
import { HelpContent } from './help-content';

export function Documents() {
  return <DocumentLayout>{bootstrap.page === 'about' ? <About /> : bootstrap.page === 'help' ? <HelpContent paths={bootstrap.paths || {}} /> : <Terms />}</DocumentLayout>;
}
function About() {
  const build = bootstrap.build;
  return <><p>关于 Still Wanna Dance</p><Title2 as="h1">Load failed? But I still wanna dance!</Title2><p>Still Wanna Dance 是面向 VRChat WannaDance 的本地视频存储与预加载工具，帮助你管理本地视频、提前准备歌曲。</p>
    <Panel title="从准备到开跳"><p>把视频准备交给 Still Wanna Dance，把时间留给跳舞。</p><dl>
      <dt>提前准备</dt><dd>根据房间队列预加载即将播放的歌曲，也可以在「准备歌曲」中使用「下载补齐」。</dd>
      <dt>重复利用</dt><dd>已保存的本地视频可在后续播放时复用，减少重复下载。</dd>
      <dt>轻松管理</dt><dd>查看下载进度与本地视频占用，设置视频存储预算，让本地空间保持有序。</dd>
    </dl></Panel>
    {build && <Panel title="版本与构建"><dl>
      <dt>版本</dt><dd>{build.version}</dd>
      <dt>构建类型</dt><dd>{build.kind === 'formal' ? '正式发行' : build.kind === 'rebuild' ? '对应源码重建' : '开发构建'}{build.dirty ? '（含未提交修改或无法确认源码状态）' : ''}</dd>
      <dt>源码提交</dt><dd>{build.revision || '未记录'}</dd>
      <dt>构建时间（UTC）</dt><dd>{build.builtAtUtc || '未记录'}</dd>
      <dt>目标平台</dt><dd>{build.target}</dd><dt>Go 工具链</dt><dd>{build.toolchain}</dd>
    </dl></Panel>}
    <Panel title="特别感谢"><ul><li>华年忆梦</li><li>逻辑XR</li><li>小新喵</li></ul></Panel>
    <p><Link href="/terms">使用条款与内容权利声明</Link> · 当前软件发行采用 GNU GPLv3（自有新贡献 GPL-3.0-only），历史 MIT 授权仍有效，第三方视频、音乐等内容不在授权范围内。</p>
    <small>© 2026 Still Wanna Dance contributors. GNU GPLv3.</small></>;
}
function Terms() {
  const [message, setMessage] = useState(''), [error, setError] = useState(false), [busy, setBusy] = useState(false);
  async function act(path: string, success: string) {
    if (busy) return;
    const hosts = path.startsWith('hosts/');
    setBusy(true); setError(false); setMessage(hosts ? hostsActionPending : '正在处理…');
    try { await request(path, {}, AbortSignal.timeout(hosts ? hostsActionTimeout : 30000)); setMessage(success); }
    catch (e) { setMessage(e instanceof APIError ? e.message : unconfirmedAction); setError(true); }
    finally { setBusy(false); }
  }
  return <><Title2 as="h1">使用条款与内容权利声明</Title2><p>本机已确认当前条款。<Link href="/">返回控制台</Link></p><p>软件许可不等于内容授权。</p>
    <p>条款版本：{bootstrap.terms?.version} · <Link href="/terms.txt" target="_blank" rel="noopener">查看／保存纯文本</Link></p>
    <Card role="article" appearance="outline" size="large" className="terms-text" tabIndex={0} aria-label="完整使用条款">{bootstrap.terms?.text}</Card>
    <div className="actions"><Button disabled={busy} onClick={() => void act('hosts/disable', 'hosts 恢复操作已完成。')}>恢复游戏直连</Button><Button disabled={busy} onClick={() => void act('stop', '播放网关已停止。')}>停止播放网关</Button></div>
    <Feedback error={error}>{message}</Feedback></>;
}
