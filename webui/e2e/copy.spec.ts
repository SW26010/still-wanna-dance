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

test('key consequences stay near their related choices',async({page})=>{
  await mockAPI(page);await page.goto('/#storage-settings');
  await expect(page.locator('#storage-settings').getByText('运行配置保存后重启生效。',{exact:true})).toBeVisible();
  await page.evaluate(()=>{location.hash='video-import';});await expect(page.getByText(/请勿修改源视频/)).toBeVisible();
  await page.evaluate(()=>{location.hash='network-proxy';});
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await expect(page.getByText('留空保留已有密码；清空用户名可移除认证。',{exact:true})).toBeVisible();
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  await expect(page.getByText('检查仅生成草稿；保存后替换整份节点库，重启生效。',{exact:true})).toBeVisible();
  await navigate(page,'网络');await page.getByRole('link',{name:'接入诊断',exact:true}).click();await expect(page.getByRole('button',{name:'修复并开启加速',exact:true})).toBeVisible();
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
