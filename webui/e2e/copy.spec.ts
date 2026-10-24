import {navigate,navigateStep} from './navigation';
import {test,expect,type Page} from '@playwright/test';
import {mockAPI} from './api';
import baseline from './baseline-copy.json' with {type:'json'};

// The migration snapshot remains historical evidence. UX revisions deliberately
// change prose; protect material consequences instead of every old sentence.
const compact=(value:string)=>value.replace(/\s/g,'');
async function textWithDetails(page:Page) {
  const closed=page.locator('.fui-AccordionHeader button[aria-expanded="false"]:visible');
  while(await closed.count())await closed.first().click();
  return compact(await page.locator('body').innerText());
}

test('connection diagnostics explain acceleration and exit effects',async({page})=>{
  await mockAPI(page);await page.goto('/');
  await expect(page.getByText(/队列按设置随网关启用/)).toBeVisible();
  await page.evaluate(()=>{location.hash='network-access';});await expect(page.getByRole('tab',{name:'高级接入',exact:true})).toHaveAttribute('aria-selected','true');
  const text=await textWithDetails(page);
  for(const consequence of ['接入诊断复用一键加速流程','冲突不会覆盖其他程序的映射','收到请求不代表游戏已播放成功'])expect(text).toContain(consequence);
});

test('settings retain storage, restart and credential consequences in the appropriate details',async({page})=>{
  await mockAPI(page);await page.goto('/#storage-settings');
  await expect(page.getByRole('region',{name:'设置操作',exact:true})).toBeVisible();
  let text=await textWithDetails(page);
  for(const consequence of ['保存并重启应用后生效','批量任务需手动重新开始'])expect(text).toContain(consequence);
  await page.evaluate(()=>{location.hash='video-import';});await expect(page.getByText(/请勿修改源视频/)).toBeVisible();
  await page.evaluate(()=>{location.hash='network-proxy';});
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  text=await textWithDetails(page);
  expect(text).toContain('已有密码留空');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  text=await textWithDetails(page);
  for(const consequence of ['替换整份节点库','重启生效','Windows当前用户加密保护'])expect(text).toContain(consequence);
});

test('license attribution and explicit consent remain available',async({page})=>{
  await mockAPI(page);await page.goto('/about');
  const text=await textWithDetails(page);
  for(const paragraph of baseline.paragraphs['about.html'].filter(p=>!p.includes('准备歌曲')&&!p.includes('下载管理')))expect(text).toContain(compact(paragraph));
  await navigate(page,'使用条款');
  await expect(page.getByRole('article',{name:'完整使用条款'})).toContainText('浏览器测试条款');
  await page.goto('/#/setup');await navigateStep(page,'3. 网络');
  await page.getByRole('checkbox',{name:'直连',exact:true}).uncheck();
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await expect(page.getByText('请至少选择一种连接方式。',{exact:true})).toBeVisible();
});
