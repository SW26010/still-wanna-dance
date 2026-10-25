import { test, expect, type Page } from '@playwright/test';
import { mockAPI } from './api';
import { navigate } from './navigation';

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

test('VRCX keeps the selected database and preview across tool return and page navigation',async({page})=>{
  const {calls}=await fixture(page,3);
  await page.goto('/#/library');
  const history=page.getByRole('button',{name:'导入 VRCX 历史',exact:true});
  await expect(history).toBeVisible();
  expect(calls).toHaveLength(0);
  await history.click();await enable(page);
  await page.getByRole('button',{name:'选择数据库',exact:true}).click();
  const path=page.getByLabel('VRCX 数据库路径'), preview=page.getByText('4 次 / 2 首',{exact:true});
  await expect(path).toHaveValue('D:\\custom\\VRCX.sqlite3');
  await inspect(page);await expect(preview).toBeVisible();
  await page.getByRole('button',{name:'返回曲库',exact:true}).click();await expect(preview).toBeHidden();
  await history.click();await expect(preview).toBeVisible();
  await page.evaluate(()=>{location.hash='cache';});
  await expect(page.getByRole('table',{name:'曲库资源',exact:true})).toBeVisible();
  await history.click();await expect(preview).toBeVisible();await expect(path).toHaveValue('D:\\custom\\VRCX.sqlite3');
  await navigate(page,'加速');await navigate(page,'曲库');
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
  const {calls}=await fixture(page);await page.goto('/#/library');await page.getByRole('button',{name:'导入 VRCX 历史',exact:true}).click();
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
  const {calls}=await fixture(page,3);await page.goto('/#/library');await page.getByRole('button',{name:'导入 VRCX 历史',exact:true}).click();await enable(page);await inspect(page);
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
  await page.goto('/#/library');await page.getByRole('button',{name:'导入 VRCX 历史',exact:true}).click();await enable(page);
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
  await page.goto('/#/library');await page.getByRole('button',{name:'导入 VRCX 历史',exact:true}).click();await enable(page);await inspect(page);
  await page.getByRole('button',{name:'导入历史偏好',exact:true}).click();
  await expect(page.getByText(/已有歌曲需求统计；请重新预览/)).toBeVisible();
  await expect(page.getByRole('button',{name:'导入历史偏好',exact:true})).toHaveCount(0);
  expect(calls.filter(c=>c.path==='preview')).toHaveLength(1);
});

for (const width of [390,1280]) test(`VRCX preview layout at ${width}px`,async({page})=>{
  await page.setViewportSize({width,height:950});await page.emulateMedia({colorScheme:width===390?'dark':'light'});
  const {report}=await fixture(page,7);
  report.source='C:\\'+('very-long-source-directory\\'.repeat(8))+'VRCX.sqlite3';
  await page.goto('/#/library');await page.getByRole('button',{name:'导入 VRCX 历史',exact:true}).click();await enable(page);await inspect(page);
  await expect(page.getByText('4 次 / 2 首',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'导入并覆盖…',exact:true}).scrollIntoViewIfNeeded();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:`../artifacts/issue28-vrcx-${width}.png`,fullPage:false});
});
