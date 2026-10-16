import {navigate,navigateStep} from './navigation';
import {test,expect,type Page} from '@playwright/test';
import {mockAPI} from './api';
import baseline from './baseline-copy.json' with {type:'json'};

// These paragraphs were extracted from the pre-migration templates at the
// recorded commit, not copied from the React implementation under test.
const compact=(value:string)=>value.replace(/\s/g,'');
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
  for(const paragraph of baseline.paragraphs['index.html'])expect(text,paragraph).toContain(compact(paragraph));
});
test('documents and wizard retain baseline prose and labels',async({page})=>{
  await mockAPI(page);
  for(const file of ['about.html','help.html'] as const){await page.goto('/'+file.replace('.html',''));const text=await textWithDetails(page);for(const paragraph of baseline.paragraphs[file])expect(text,paragraph).toContain(compact(paragraph));}
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
