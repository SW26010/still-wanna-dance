import { Link, Title2, Card, Tab, TabList } from '@fluentui/react-components';
import { bootstrap } from './bootstrap';
import { DocumentLayout, Panel, isDocumentPage, pageHref, type DocumentPage } from './ui';
import { HelpContent } from './help-content';

export function Documents() {
  return <DocumentLayout>{isDocumentPage(bootstrap.page)&&<DocumentContent page={bootstrap.page}/>}</DocumentLayout>;
}
export function DocumentContent({page,paths=bootstrap.paths || {}}:{page:DocumentPage;paths?:Record<string,string>}) {
  return page==='about'?<About/>:page==='help'?<HelpContent paths={paths}/>:<Terms/>;
}
function aboutTab(anchor:string) {return anchor.includes('help')?'help':anchor.includes('terms')?'terms':anchor.includes('licenses')?'licenses':'intro';}
export function AboutPage({anchor,paths,onTabSelect}:{anchor:string;paths:Record<string,string>;onTabSelect:(anchor:string)=>void}) {
  const tab=aboutTab(anchor);
  return <><TabList style={{flexWrap:'wrap'}} selectedValue={tab} onTabSelect={(_,v)=>onTabSelect(({intro:'/about',help:'/help',terms:'/terms',licenses:'/licenses'} as Record<string,string>)[String(v.value)])} aria-label="关于内容"><Tab id="about-intro" aria-controls="about-panel" value="intro">介绍</Tab><Tab id="about-help" aria-controls="about-panel" value="help">使用帮助</Tab><Tab id="about-terms" aria-controls="about-panel" value="terms">用户协议</Tab><Tab id="about-licenses" aria-controls="about-panel" value="licenses">许可证与声明</Tab></TabList>
    <div id="about-panel" role="tabpanel" aria-labelledby={'about-'+tab} tabIndex={0}>{tab==='intro'?<About heading="h2"/>:tab==='help'?<HelpContent paths={paths} heading="h2"/>:tab==='terms'?<Terms heading="h2"/>:<Panel title="许可证与第三方声明"><p>软件发行采用 GNU GPLv3，自有新贡献为 GPL-3.0-only；历史 MIT 授权仍有效。第三方组件按各自声明保留授权，软件许可不授予视频、音乐等内容的权利。</p><ul><li><Link href="/assets/webui/software-license.txt" target="_blank" rel="noopener">软件许可证原文</Link></li><li><Link href="/assets/webui/third-party-notices.txt" target="_blank" rel="noopener">程序第三方声明原文</Link></li><li><Link href="/assets/webui/webui-notices.txt" target="_blank" rel="noopener">WebUI 第三方声明原文</Link></li></ul><p>发行包的 licenses/ 还包含实际构建工具链声明及 SOURCE.txt 对应源码说明。重新分发时保留整个目录；离线分发同时附上匹配的完整对应源码。</p></Panel>}</div></>;
}
function About({heading='h1'}:{heading?:'h1'|'h2'}={}) {
  const build = bootstrap.build;
  return <><Title2 as={heading}>Load failed? But I still wanna dance!</Title2><p>Still Wanna Dance 是面向 VRChat WannaDance 的本地视频存储与预加载工具。</p>
    <Panel title="从准备到开跳"><p>把视频准备交给 Still Wanna Dance，把时间留给跳舞。</p><dl>
      <dt>提前准备</dt><dd>根据房间队列预加载即将播放的歌曲，也可以在「曲库」中使用「下载缺失视频」。</dd>
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
    <p><Link href={pageHref('terms')}>使用条款与内容权利声明</Link> · 当前软件发行采用 GNU GPLv3（自有新贡献 GPL-3.0-only），历史 MIT 授权仍有效，第三方视频、音乐等内容不在授权范围内。</p>
    <small>© 2026 Still Wanna Dance contributors. GNU GPLv3.</small></>;
}
function Terms({heading='h1'}:{heading?:'h1'|'h2'}={}) {
  return <><Title2 as={heading}>使用条款与内容权利声明</Title2><p>本机已确认当前条款。<Link href={pageHref('home')}>返回首页</Link></p><p>软件许可不等于内容授权。</p>
    <p>条款版本：{bootstrap.terms?.version} · <Link href="/terms.txt" target="_blank" rel="noopener">查看／保存纯文本</Link></p>
    <Card role="article" appearance="outline" size="large" className="terms-text" tabIndex={0} aria-label="完整使用条款">{bootstrap.terms?.text}</Card>
    </>;
}
