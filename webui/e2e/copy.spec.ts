import {navigate,navigateStep} from './navigation';
import {test,expect,type Page} from '@playwright/test';
import {mockAPI} from './api';
import baseline from './baseline-copy.json' with {type:'json'};

// These paragraphs were extracted from the pre-migration templates at the
// recorded commit, not copied from the React implementation under test.
const compact=(value:string)=>value.replace(/\s/g,'');
// Preserve that historical snapshot while spelling out intentional changes.
// Unchanged portions of each paragraph remain covered by the baseline check.
const copyUpdates = [
  ['默认 3，范围 1～100。准备队列前 N 个位置中的有效曲目。', '按预计播放时间提前准备队列中的有效曲目。'],
  ['默认 500 秒，范围 1～86400。', '默认 500 秒，建议 200～2000 秒（当前值约占 125 MB 临时缓存，按 2 Mbps 估算）。'],
  ['网络环境检测固定标注「直连」，刷新按钮统一重测歌曲列表、播放地址及视频下载，不验证代理节点；下方「连接方式」选择直连或代理。',
    '在网络页选择直连或代理，再手动检测连通性或测速；检测使用当前草稿，不会自动保存配置。'],
  ['容量上限为 0 时不限，使用中的视频和临时文件可能暂时超限。',
    '默认限制为 0 GiB，不持久保留缓存；仍会临时写盘，使用和队列保护结束后回收视频。选择「不限制」才不限容量，正数限制持久缓存预算；使用中的视频和临时文件可能暂时超限。'],
  ['向导重入会直接检测直连速度，即使业务使用强制代理，也不改变业务配置。',
    '引导中的连通性检测和测速由你手动启动，使用所选连接方式和代理草稿，不改变已保存的业务配置。'],
  ['构建信息见 build-info.json，发行 ZIP 的完整性见旁边 .sha256 文件。许可和第三方声明位于 licenses/；升级和重新分发应保留并更新。软件许可不授予视频、音乐的内容权利。',
    '版本、源码提交和构建信息见「关于」，发行 ZIP 的完整性见旁边 .sha256 文件。完整条款可在「使用条款」中离线查看或保存纯文本。许可和第三方声明位于 licenses/；升级和重新分发应保留并更新。正式版的准确对应源码入口和校验见 licenses/SOURCE.txt，源码附件与 Portable 位于同一个 Release；离线再分发请同时附匹配的完整对应源码。软件许可不授予视频、音乐的内容权利。'],
] as const;
const currentCopy=(paragraph:string)=>copyUpdates.reduce((text,[before,after])=>text.replace(before,after),paragraph);
async function textWithDetails(page:Page) {
  const closed=page.locator('.fui-AccordionHeader button[aria-expanded="false"]:visible');
  while(await closed.count())await closed.first().click();
  return compact(await page.locator('body').innerText());
}
test('console preserves baseline explanations across every page and proxy backend',async({page})=>{
  const api=await mockAPI(page);api.state.batch.budgetReached=true;await page.goto('/');let text='';
  for(const name of ['开始使用','准备歌曲','本地缓存','运行状态','设置']){
    await navigate(page,name);text+=await textWithDetails(page);
  }
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');text+=await textWithDetails(page);
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');text+=await textWithDetails(page);
  // Saved-credential hint belongs to the wizard's proxy fields.
  await page.goto('/#/setup');await navigateStep(page,'3. 网络');
  await page.getByRole('checkbox',{name:'代理',exact:true}).check();text+=await textWithDetails(page);
  // Zero capacity now means no retention; the pre-migration sentinel explanation is intentionally replaced.
  for(const paragraph of baseline.paragraphs['index.html'].filter(p=>!p.startsWith('0 表示不限。')))expect(text,paragraph).toContain(compact(currentCopy(paragraph)));
});
test('documents and wizard retain baseline prose and labels',async({page})=>{
  await mockAPI(page);
  for(const file of ['about.html','help.html'] as const){await page.goto('/'+file.replace('.html',''));const text=await textWithDetails(page);for(const paragraph of baseline.paragraphs[file])expect(text,paragraph).toContain(compact(currentCopy(paragraph)));}
  await page.goto('/#/setup');let text=await textWithDetails(page);
  await expect(page.getByText('专为 VRChat WannaDance 设计的高性能视频加速器',{exact:true})).toBeVisible();
  await navigateStep(page,'2. 存储');text+=await textWithDetails(page);
  await page.getByRole('checkbox',{name:'导入已有视频',exact:true}).check();
  await expect(page.getByText('请放心，我们不会修改来源文件，并且会尽量使用硬链接来避免双份空间占用。导入后请勿修改源视频。',{exact:true})).toBeVisible();
  await page.getByRole('checkbox',{name:'导入已有视频',exact:true}).uncheck();
  await navigateStep(page,'3. 网络');text+=await textWithDetails(page);
  for(const paragraph of baseline.paragraphs['setup.html'])expect(text,paragraph).toContain(compact(paragraph));
  await page.getByRole('checkbox',{name:'直连',exact:true}).uncheck();
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await expect(page.getByText('请至少选择一种连接方式。',{exact:true})).toBeVisible();
});
