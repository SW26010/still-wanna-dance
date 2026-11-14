import { Link, Subtitle2, Title2, Tooltip } from '@fluentui/react-components';
import type { PropsWithChildren } from 'react';
import { Panel, pageHref, sectionHref } from './ui';
import { DiagnosticExport } from './diagnostic-export';

function Term({children,description}:PropsWithChildren<{description:string}>) {
  return <Tooltip content={description} relationship="description"><span className="help-term" tabIndex={0}>{children}</span></Tooltip>;
}

export function HelpContent({heading='h1'}:{heading?:'h1'|null}) {
  return <article className="help-guide">
    {heading&&<Title2 as={heading}>帮助</Title2>}
    <Panel id="help-start" title="启动与退出">
      <p>完整解压 ZIP，运行 SWD.exe 并同意条款。在<Link href={pageHref('home')}>加速</Link>页点击「开始加速」，按提示完成管理员授权，然后在游戏里点歌。</p>
      <p>关闭网页后仍会继续加速，需要时从系统托盘重新打开。</p>
      <p id="help-stop" tabIndex={-1}>「停止加速」暂停游戏加速，独立下载仍会继续；「退出程序」结束后台工作。默认恢复游戏直连，可在<Link href={sectionHref('restoreHostsOnStopOrExit')}>停止与退出设置</Link>调整。关闭恢复后，下次游戏前需启动 SWD 并开启加速。</p>
    </Panel>
    <Panel id="help-playback" title="视频加载不出来，先做什么？">
      <ol className="help-steps">
        <li><Subtitle2 as="h3">先确认加速已开启</Subtitle2>
          <p>到<Link href={sectionHref('activation')}>加速状态</Link>查看「游戏连接」和「视频服务」。有错误先按提示处理；开启后重新点歌。仍没收到请求时，再尝试重进世界或重启游戏。</p>
        </li>
        <li><Subtitle2 as="h3">已经在下载，但一直很慢</Subtitle2>
          <p>到<Link href={pageHref('network')}>网络</Link>检查连接和下载速度。当前连接慢时，尝试更换<Term description="SWD 下载视频所用的网络连接，可以是直连或代理。">上游连接</Term>。</p>
          <p>在<Link href={sectionHref('network-proxy')}>设置 → 网络</Link>配置代理：已有代理软件可使用<Term description="填写代理软件提供的 SOCKS5 地址和端口，让 SWD 通过它下载。">SOCKS5</Term>；有节点配置文件可使用<Term description="SWD 内置的代理工具。导入节点配置后，在网络页的代理面板选择节点。">Mihomo</Term>。保存后按提示重启；已经使用代理时，可尝试其他节点。</p>
          <p>到<Link href={sectionHref('downloads')}>视频需求</Link>看下载进度；只有个别歌曲失败时，查看<Link href={pageHref('timeline')}>请求历史</Link>中的原因，也检查歌曲是否被屏蔽。</p>
        </li>
        <li><Subtitle2 as="h3">减少下次播放时的等待</Subtitle2>
          <p>开启<Link href={sectionHref('storage-settings')}>视频保留</Link>，让下载过的视频留在电脑里；常用歌曲也可以在<Link href={pageHref('library')}>曲库</Link>中提前下载、预览，或<Link href={sectionHref('batch')}>批量下载缺失视频</Link>。</p>
          <p><Term description="根据当前房间的点歌队列，提前下载即将播放的视频。">房间队列预加载</Term>能提前准备后面的歌曲。在<Link href={sectionHref('download-settings')}>下载与缓存</Link>中开启；提示找不到日志时，选择 VRChat 日志目录。</p>
        </li>
      </ol>
    </Panel>
    <Panel id="help-storage" title="留多少空间，哪些视频会保存？">
      <p>到<Link href={sectionHref('storage-settings')}>设置 → 存储</Link>，选择「保留下载的视频」：</p>
      <ul>
        <li><strong>不保留：</strong>边播边下载，不长期保存视频。</li>
        <li><strong>限制为：</strong>填入愿意分配的空间。参考旁边的<Term description="估计保存这些视频后，有多少播放请求能直接使用本地视频；数值越高，重复下载通常越少。">预计请求命中率</Term>调整，给磁盘留一些余量。</li>
        <li><strong>不限制：</strong>持续保留视频，适合空间充足时使用。</li>
      </ul>
      <p>保存后重启生效。空间不够时，可降低保留额度，或到曲库删除不需要的本地视频；正在使用的视频稍后再删。</p>
      <Subtitle2 as="h3">常用的优先留下</Subtitle2>
      <p>程序优先保留近期常用的歌曲，空间不足时清理优先级较低的视频。开启「优先保留收藏视频」后，收藏优先；在曲库里收藏常玩的歌曲，想提前准备时再点下载。</p>
      <p>不想播放的歌曲可以屏蔽，SWD 会跳过它的播放和下载。用过<Term description="VRChat 辅助工具。导入其中的 WannaDance 播放记录，可帮助 SWD 了解你常玩的歌曲。">VRCX</Term>的话，可以<Link href={sectionHref('vrcx-import')}>导入历史</Link>，更快建立常用歌曲的保留顺序。</p>
      <Subtitle2 as="h3">已有视频不用重新下载</Subtitle2>
      <p>使用<Link href={sectionHref('video-import')}>导入视频</Link>选择已有视频目录，查看空间预估后开始。导入可以暂停，退出程序后需手动继续。请勿修改源视频。</p>
    </Panel>
    <Panel title="备份与升级">
      <ul>
        <li><strong>备份或搬家：</strong>先正常退出，备份配置文件和数据库所在目录，视频按需备份。更换存储位置不自动搬移文件，搬好后在存储设置中选择新位置，保存并重启。</li>
        <li><strong>升级：</strong>退出程序，将新版 SWD.exe 和整个 licenses 文件夹覆盖到原目录，保留其他文件，再启动。</li>
        <li><strong>卸载：</strong>开启并保存「停止加速或退出时恢复游戏直连」，退出并确认恢复成功，再备份数据、删除程序文件夹。外置的视频和数据库按需保留。</li>
      </ul>
    </Panel>
    <Panel id="help-paths" title="反馈问题">
      <p>遇到问题时，可下载诊断包用于反馈，包含版本信息、所选时间段的请求历史及 SWD、VRChat 日志。日志级别可在<Link href={sectionHref('preferences')}>常规设置</Link>调整。</p>
      <DiagnosticExport/>
    </Panel>
  </article>;
}
