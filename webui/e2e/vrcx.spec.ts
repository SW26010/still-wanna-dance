import { test, expect, type Page } from '@playwright/test';
import { mockAPI } from './api';
import { navigate, navigateStep } from './navigation';

async function fixture(page: Page, existing = 0) {
  const api = await mockAPI(page);
  const report = {id:'preview-1',source:'C:\\VRCX\\VRCX.sqlite3',storageDir:api.state.activeSettings.storageDir,
    total:9,recognized:7,events:4,duplicates:2,invalid:1,unrelated:2,skipped:5,songs:2,
    firstAt:Date.UTC(2025,0,1),lastAt:Date.UTC(2026,0,1),existingSongs:existing,imported:false};
  const calls: {path:string;body:Record<string,unknown>}[] = [];
  await page.route('**/api/vrcx/**',async route => {
    const path = new URL(route.request().url()).pathname.split('/').at(-1)!, body = route.request().postDataJSON();
    calls.push({path,body});
    if(path==='detect')return route.fulfill({json:{path:report.source,found:true,existingSongs:report.existingSongs}});
    if(path==='select')return route.fulfill({json:{path:'D:\\custom\\VRCX.sqlite3',canceled:false}});
    if(path==='preview') { if(body.storageDir)report.storageDir=body.storageDir; return route.fulfill({json:report}); }
    if(path==='apply')return route.fulfill({json:{...report,imported:true}});
    return route.fulfill({json:{ok:true}});
  });
  return {api,report,calls};
}
async function enable(page: Page) { await page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true}).check(); }
async function inspect(page: Page) { await page.getByRole('button',{name:'读取并预览',exact:true}).click(); }

test('VRCX keeps the selected database and preview across folding and page navigation',async({page})=>{
  const {calls}=await fixture(page,3);
  await page.goto('/#/library');
  const history=page.getByRole('button',{name:'根据历史选择下载歌曲',exact:true});
  await expect(history).toBeVisible();
  expect(calls).toHaveLength(0);
  await history.click();await enable(page);
  await page.getByRole('button',{name:'选择数据库',exact:true}).click();
  const path=page.getByLabel('VRCX 数据库路径'), preview=page.getByText('4 次 / 2 首',{exact:true});
  await expect(path).toHaveValue('D:\\custom\\VRCX.sqlite3');
  await inspect(page);await expect(preview).toBeVisible();
  await history.click();await expect(preview).toBeHidden();
  await history.click();await expect(preview).toBeVisible();
  await navigate(page,'本地视频');await navigate(page,'下载管理');
  await expect(page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true})).toBeChecked();
  await expect(path).toHaveValue('D:\\custom\\VRCX.sqlite3');
  await expect(preview).toBeVisible();
  expect(calls.map(c=>c.path)).toEqual(['detect','select','preview']);
  await page.getByRole('button',{name:'导入并覆盖…',exact:true}).click();
  await page.getByRole('button',{name:'强制导入并覆盖',exact:true}).click();
  await expect(page.getByText(/已导入 4 次历史需求/)).toBeVisible();
  expect(calls.filter(c=>c.path==='apply')).toEqual([{path:'apply',body:{id:'preview-1',overwrite:true}}]);
});

test('VRCX reads only on explicit preview and empty usage imports without overwrite',async({page})=>{
  const {calls}=await fixture(page);await page.goto('/#/library');await page.getByRole('button',{name:'根据历史选择下载歌曲',exact:true}).click();
  await expect(page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true})).toBeChecked();
  await expect(page.getByLabel('VRCX 数据库路径')).toHaveValue('C:\\VRCX\\VRCX.sqlite3');
  expect(calls.map(c=>c.path)).toEqual(['detect']);
  await inspect(page);await expect(page.getByText('4 次 / 2 首',{exact:true})).toBeVisible();
  await expect(page.getByText(/当前歌曲需求统计为空/)).toBeVisible();
  expect(calls.some(c=>c.path==='apply')).toBe(false);
  await page.getByRole('button',{name:'导入历史偏好',exact:true}).click();
  await expect(page.getByText('已导入 4 次历史需求、2 首歌曲，跳过 5 条记录。')).toBeVisible();
  expect(calls.filter(c=>c.path==='apply')).toEqual([{path:'apply',body:{id:'preview-1',overwrite:false}}]);
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('VRCX nonempty usage requires second confirmation and cancel leaves it untouched',async({page})=>{
  const {calls}=await fixture(page,3);await page.goto('/#/library');await page.getByRole('button',{name:'根据历史选择下载歌曲',exact:true}).click();await enable(page);await inspect(page);
  const action=page.getByRole('button',{name:'导入并覆盖…',exact:true});
  await action.click();await expect(page.getByRole('dialog')).toContainText('整体被替换，不会与 VRCX 结果累加');
  await page.getByRole('button',{name:'取消',exact:true}).click();
  expect(calls.some(c=>c.path==='apply')).toBe(false);
  await expect(action).toBeFocused();
  await action.click();await page.getByRole('button',{name:'强制导入并覆盖',exact:true}).click();
  await expect(page.getByText(/已导入 4 次历史需求/)).toBeVisible();
  expect(calls.filter(c=>c.path==='apply')[0].body.overwrite).toBe(true);
});

test('VRCX default missing, file selection, empty history and parse failures stay skippable',async({page})=>{
  const {report,calls}=await fixture(page);
  await page.route('**/api/vrcx/detect',r=>r.fulfill({json:{path:'',found:false,existingSongs:0}}));
  await page.goto('/#/library');await page.getByRole('button',{name:'根据历史选择下载歌曲',exact:true}).click();await enable(page);
  await expect(page.getByText(/未找到默认位置/)).toBeVisible();
  await page.getByRole('button',{name:'选择数据库',exact:true}).click();
  await expect(page.getByLabel('VRCX 数据库路径')).toHaveValue('D:\\custom\\VRCX.sqlite3');
  Object.assign(report,{id:'',events:0,songs:0});
  await inspect(page);await expect(page.getByText('没有有效历史需求，原统计保持不变。')).toBeVisible();
  await expect(page.getByRole('button',{name:'导入历史偏好',exact:true})).toHaveCount(0);
  await page.route('**/api/vrcx/preview',r=>r.fulfill({status:400,body:'所选 VRCX 数据库缺少 created_at 或 video_url 字段'}));
  await inspect(page);await expect(page.getByText(/缺少 created_at/)).toBeVisible();
  expect(calls.some(c=>c.path==='apply')).toBe(false);
});

test('VRCX rechecks changed target statistics and never silently retries with overwrite',async({page})=>{
  const {calls}=await fixture(page);
  await page.route('**/api/vrcx/apply',r=>r.fulfill({status:409,body:'已有歌曲需求统计；请重新预览并明确确认整表覆盖'}));
  await page.goto('/#/library');await page.getByRole('button',{name:'根据历史选择下载歌曲',exact:true}).click();await enable(page);await inspect(page);
  await page.getByRole('button',{name:'导入历史偏好',exact:true}).click();
  await expect(page.getByText(/已有歌曲需求统计；请重新预览/)).toBeVisible();
  await expect(page.getByRole('button',{name:'导入历史偏好',exact:true})).toHaveCount(0);
  expect(calls.filter(c=>c.path==='preview')).toHaveLength(1);
});

test('wizard automatically checks default VRCX and imports only with final settings application',async({page})=>{
  const {calls,api}=await fixture(page);await page.goto('/#/setup');
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await expect(page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true})).toBeChecked();
  await expect(page.getByText('根据 VRCX 历史，优先下载和保留你常用的歌曲。',{exact:true})).toBeVisible();
  await expect(page.getByLabel('VRCX 数据库路径')).toHaveCount(0);
  await expect(page.getByRole('button',{name:'选择数据库',exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'读取并预览',exact:true})).toHaveCount(0);
  expect(calls.some(c=>c.path==='preview'||c.path==='apply')).toBe(false);
  await page.getByRole('checkbox',{name:'指定其他位置（程序文件夹外）'}).check();
  await page.getByRole('button',{name:'C:\\UI-Test\\storage',exact:true}).click();
  await navigateStep(page,'4. 启用');
  await expect(page.getByText(/VRCX 歌曲偏好：检查通过，4 次需求 \/ 2 首歌曲/)).toBeVisible();
  expect(calls.find(c=>c.path==='preview')?.body.storageDir).toBe('D:\\UI-Test\\cache');
  expect(calls.find(c=>c.path==='preview')?.body.path).toBe('');
  expect(calls.some(c=>c.path==='apply')).toBe(false);
  expect(api.calls.some(c=>c.path==='setup/apply')).toBe(false);
  await page.getByRole('button',{name:'确认并应用',exact:true}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='setup/apply').length).toBe(1);
  expect(api.calls.find(c=>c.path==='setup/apply')?.body).toMatchObject({vrcxEnabled:true,vrcxPreviewID:'preview-1',vrcxOverwrite:false,storageDir:'D:\\UI-Test\\cache'});
  expect(calls.some(c=>c.path==='apply')).toBe(false);
});

test('nonempty wizard usage defaults off and warns only at final application',async({page})=>{
  const {calls,api}=await fixture(page,3);await page.goto('/#/setup');
  await navigateStep(page,'2. 存储');
  const option=page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true});
  await expect(option).toBeEnabled();await expect(option).not.toBeChecked();
  await option.check();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(calls.some(c=>c.path==='preview'||c.path==='apply')).toBe(false);
  await navigateStep(page,'4. 启用');
  await expect(page.getByText(/VRCX 歌曲偏好：检查通过/)).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  const apply=page.getByRole('button',{name:'确认并应用',exact:true});
  await apply.click();await expect(page.getByRole('dialog')).toContainText('现有 3 首歌曲');
  await page.getByRole('button',{name:'取消',exact:true}).click();
  await expect(apply).toBeFocused();
  expect(api.calls.some(c=>c.path==='setup/apply')).toBe(false);
  await apply.click();await page.getByRole('button',{name:'强制导入并覆盖',exact:true}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='setup/apply').length).toBe(1);
  expect(api.calls.find(c=>c.path==='setup/apply')?.body).toMatchObject({vrcxEnabled:true,vrcxPreviewID:'preview-1',vrcxOverwrite:true});
});

for(const existing of [0,3])for(const failure of ['missing','empty'])test(`wizard skips ${failure} VRCX check with ${existing} existing songs without blocking application`,async({page})=>{
  const {report,calls,api}=await fixture(page,existing);
  if(failure==='empty')Object.assign(report,{id:'',events:0,songs:0});
  else await page.route('**/api/vrcx/preview',r=>r.fulfill({status:400,body:'未找到默认位置的 VRCX 数据库'}));
  await page.goto('/#/setup');
  if(existing){await navigateStep(page,'2. 存储');await enable(page);}
  await navigateStep(page,'4. 启用');
  await expect(page.getByText(failure==='empty'?/VRCX 歌曲偏好：没有有效历史需求，跳过导入/:/VRCX 歌曲偏好：检查不通过，跳过导入/)).toBeVisible();
  await page.getByRole('button',{name:'确认并应用',exact:true}).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect.poll(()=>api.calls.filter(c=>c.path==='setup/apply').length).toBe(1);
  expect(api.calls.find(c=>c.path==='setup/apply')?.body).toMatchObject({vrcxEnabled:true,vrcxPreviewID:'',vrcxOverwrite:false});
  expect(calls.some(c=>c.path==='apply')).toBe(false);
});

test('wizard deselection skips reads and reset restores target-based default',async({page})=>{
  const {calls}=await fixture(page);await page.goto('/#/setup');await navigateStep(page,'2. 存储');
  const option=page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true});
  await option.uncheck();await navigateStep(page,'4. 启用');
  await expect(page.getByText('VRCX 歌曲偏好：未选择，跳过导入',{exact:true})).toBeVisible();
  expect(calls.some(c=>c.path==='preview')).toBe(false);
  await navigateStep(page,'2. 存储');
  await page.getByRole('button',{name:'恢复本页默认',exact:true}).click();await expect(option).toBeChecked();
  await navigateStep(page,'4. 启用');await expect(page.getByText(/VRCX 歌曲偏好：检查通过/)).toBeVisible();
  await page.getByRole('button',{name:'刷新接入检查',exact:true}).click();
  await expect.poll(()=>calls.filter(c=>c.path==='preview').length).toBe(2);
});

for (const width of [390,1280]) test(`VRCX preview layout at ${width}px`,async({page})=>{
  await page.setViewportSize({width,height:950});await page.emulateMedia({colorScheme:width===390?'dark':'light'});
  const {report}=await fixture(page,7);
  report.source='C:\\'+('very-long-source-directory\\'.repeat(8))+'VRCX.sqlite3';
  await page.goto('/#/library');await page.getByRole('button',{name:'根据历史选择下载歌曲',exact:true}).click();await enable(page);await inspect(page);
  await expect(page.getByText('4 次 / 2 首',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'导入并覆盖…',exact:true}).scrollIntoViewIfNeeded();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:`../artifacts/issue28-vrcx-${width}.png`,fullPage:false});
});
