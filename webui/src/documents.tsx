import { Link, Title2, Card, Tab, TabList, Tooltip } from '@fluentui/react-components';
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
    {build && <Panel title="版本与构建" action={<Tooltip content="在 GitHub 查看项目（新标签页）" relationship="label">
      <Link href="https://github.com/SW26010/still-wanna-dance" target="_blank" rel="noopener noreferrer" appearance="subtle" style={{display:'flex'}}>
        {/* GitHub Octicons mark-github-16; MIT notice in THIRD-PARTY-NOTICES.txt. */}
        <svg width="20" height="20" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" focusable="false"><path d="M6.766 11.328c-2.063-.25-3.516-1.734-3.516-3.656 0-.781.281-1.625.75-2.188-.203-.515-.172-1.609.063-2.062.625-.078 1.468.25 1.968.703.594-.187 1.219-.281 1.985-.281.765 0 1.39.094 1.953.265.484-.437 1.344-.765 1.969-.687.218.422.25 1.515.046 2.047.5.593.766 1.39.766 2.203 0 1.922-1.453 3.375-3.547 3.64.531.344.89 1.094.89 1.954v1.625c0 .468.391.734.86.547C13.781 14.359 16 11.53 16 8.03 16 3.61 12.406 0 7.984 0 3.563 0 0 3.61 0 8.031a7.88 7.88 0 0 0 5.172 7.422c.422.156.828-.125.828-.547v-1.25c-.219.094-.5.156-.75.156-1.031 0-1.64-.562-2.078-1.609-.172-.422-.36-.672-.719-.719-.187-.015-.25-.093-.25-.187 0-.188.313-.328.625-.328.453 0 .844.281 1.25.86.313.452.64.655 1.031.655s.641-.14 1-.5c.266-.265.47-.5.657-.656"/></svg>
      </Link>
    </Tooltip>}><dl>
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
