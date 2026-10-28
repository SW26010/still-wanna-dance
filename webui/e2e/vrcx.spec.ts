import { test, expect, type Page } from '@playwright/test';
import { mockAPI } from './api';
import { navigate } from './navigation';

async function fixture(page: Page, existing = 0, suggested = false) {
  const api = await mockAPI(page);
  const report = {id:'preview-1',source:'C:\\VRCX\\VRCX.sqlite3',databasePath:api.state.activeSettings.databasePath,
    total:9,recognized:7,events:4,duplicates:2,invalid:1,unrelated:2,skipped:5,songs:2,
    firstAt:Date.UTC(2025,0,1),lastAt:Date.UTC(2026,0,1),existingSongs:existing,imported:false};
  const calls: {path:string;body:Record<string,unknown>}[] = [];
  await page.route('**/api/vrcx/**',async route => {
    const path = new URL(route.request().url()).pathname.split('/').at(-1)!, body = route.request().postDataJSON();
    calls.push({path,body});
    if(path==='detect')return route.fulfill({json:{path:report.source,found:true,suggested:suggested&&report.existingSongs===0,notify:suggested&&report.existingSongs===0,existingSongs:report.existingSongs}});
    if(path==='dismiss'){suggested=false;return route.fulfill({json:{ok:true}});}

    if(path==='preview') return route.fulfill({json:report});
    if(path==='apply'){suggested=false;return route.fulfill({json:{...report,imported:true}});}
    return route.fulfill({json:{ok:true}});
  });
  await page.route('**/api/path/select', route => {
    calls.push({path:'select',body:route.request().postDataJSON()});
    return route.fulfill({json:{path:'D:\\custom\\VRCX.sqlite3',canceled:false}});
  });
  return {api,report,calls};
}
async function enable(page: Page) { await page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true}).check(); }
async function inspect(page: Page) { await page.getByRole('button',{name:'读取并预览',exact:true}).click(); }

for (const width of [390,1280]) test(`first library offers VRCX import without reading history at ${width}px`,async({page})=>{
  await page.setViewportSize({width,height:950});
  await page.emulateMedia({colorScheme:width===390?'dark':'light'});
  const {calls,api}=await fixture(page,0,true);
  api.terms.accepted=false;
  await page.goto('/?gate=1');
  const consent=page.getByRole('alertdialog');
  await expect(consent).toBeVisible();
  expect(calls).toHaveLength(0);
  await consent.getByRole('button',{name:'同意完整条款',exact:true}).click();
  await expect(page.getByRole('heading',{name:'加速',exact:true})).toBeVisible();
  const notice=page.getByRole('listitem',{name:'检测到 VRCX 数据库',exact:true});
  await expect(notice).toBeVisible();
  expect(calls.map(c=>c.path)).toEqual(['detect']);
  await page.screenshot({path:`../artifacts/vrcx-suggestion/toast-${width}.png`,animations:'disabled'});
  await notice.getByRole('link',{name:'前往曲库',exact:true}).click();
  await expect(notice).toHaveCount(0);
  const action=page.getByRole('button',{name:'导入历史',exact:true});
  await expect(action).toBeVisible();
  expect(calls.map(c=>c.path)).toEqual(['detect']);
  await expect(page.getByText('检测到 VRCX 历史，可用于安排歌曲下载与保留顺序。仅在本机处理。')).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await expect(page.getByRole('button',{name:'查找',exact:true})).toBeEnabled();
  await page.screenshot({path:`../artifacts/vrcx-suggestion/suggestion-${width}.png`,animations:'disabled'});
  await action.focus();await page.keyboard.press('Enter');
  await expect(page.getByRole('listitem',{name:'已根据 VRCX 历史初始化 2 首歌曲的优先级。',exact:true})).toBeVisible();
  await expect(action).toHaveCount(0);
  await expect(page.locator('#library-browser')).toBeFocused();
  expect(calls.filter(c=>c.path==='preview'||c.path==='apply')).toEqual([
    {path:'preview',body:{path:''}},{path:'apply',body:{id:'preview-1',overwrite:false}},
  ]);
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('closing the startup toast does not import or dismiss the library suggestion and reload does not repeat it',async({page})=>{
  const {calls}=await fixture(page,0,true);await page.goto('/');
  const notice=page.getByRole('listitem',{name:'检测到 VRCX 数据库',exact:true});
  await expect(notice).toBeVisible();
  await notice.getByRole('button',{name:'关闭通知',exact:true}).click();
  await expect(notice).toHaveCount(0);
  await page.reload();await expect.poll(()=>calls.filter(c=>c.path==='detect').length).toBe(2);
  await navigate(page,'曲库');
  await expect(page.getByRole('button',{name:'导入历史',exact:true})).toBeVisible();
  await navigate(page,'加速');await expect(notice).toHaveCount(0);
  expect(calls.map(c=>c.path)).toEqual(['detect','detect']);
});

test('a toast that times out stays dismissed across navigation',async({page})=>{
  const {calls}=await fixture(page,0,true);await page.goto('/');
  const notice=page.getByRole('listitem',{name:'检测到 VRCX 数据库',exact:true});
  await expect(notice).toBeVisible();
  await page.bringToFront();
  await expect(notice).toHaveCount(0,{timeout:12000});
  await navigate(page,'历史');await navigate(page,'加速');
  await expect(notice).toHaveCount(0);
  expect(calls.map(c=>c.path)).toEqual(['detect']);
});

for(const condition of ['missing','existing','unavailable'] as const)test(`startup toast stays quiet when ${condition}`,async({page})=>{
  const {calls}=await fixture(page,condition==='existing'?3:0,false);
  if(condition==='missing')await page.route('**/api/vrcx/detect',r=>r.fulfill({json:{found:false,suggested:false,existingSongs:0}}));
  if(condition==='unavailable')await page.route('**/api/vrcx/detect',r=>r.fulfill({status:500,body:'无法检查目标歌曲统计'}));
  const detected=page.waitForResponse('**/api/vrcx/detect');
  await page.goto('/');await detected;
  await navigate(page,'曲库');await expect(page.getByRole('button',{name:'导入 VRCX 历史',exact:true})).toBeVisible();
  await expect(page.getByRole('listitem',{name:'检测到 VRCX 数据库',exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'导入历史',exact:true})).toHaveCount(0);
  expect(calls.some(c=>c.path==='preview'||c.path==='apply')).toBe(false);
});

test('ignoring VRCX suggestion survives navigation and reload without reading the source',async({page})=>{
  const {calls}=await fixture(page,0,true);await page.goto('/#/library');
  await expect(page.getByRole('button',{name:'忽略',exact:true})).toBeVisible();
  await expect(page.getByRole('listitem',{name:'检测到 VRCX 数据库',exact:true})).toHaveCount(0);
  await page.getByRole('button',{name:'忽略',exact:true}).click();
  await expect(page.getByRole('button',{name:'导入历史',exact:true})).toHaveCount(0);
  await expect(page.locator('#library-browser')).toBeFocused();
  await navigate(page,'加速');await navigate(page,'曲库');await page.reload();
  await expect(page.getByRole('button',{name:'导入 VRCX 历史',exact:true})).toBeVisible();
  await expect.poll(()=>calls.filter(c=>c.path==='detect').length).toBe(2);
  expect(calls.map(c=>c.path)).toEqual(['detect','dismiss','detect']);
  await expect(page.getByRole('button',{name:'导入历史',exact:true})).toHaveCount(0);
});

test('an empty reopened library keeps its suggestion without another startup toast',async({page})=>{
  const {calls,report}=await fixture(page,0,true);
  let detections=0;
  await page.route('**/api/vrcx/detect',r=>{detections++;return r.fulfill({json:{path:report.source,found:true,suggested:true,notify:false,existingSongs:0}});});
  await page.goto('/');await expect.poll(()=>detections).toBe(1);
  await expect(page.getByRole('listitem',{name:'检测到 VRCX 数据库',exact:true})).toHaveCount(0);
  await navigate(page,'曲库');await expect(page.getByRole('button',{name:'导入历史',exact:true})).toBeVisible();
  await page.reload();await expect.poll(()=>detections).toBe(2);
  await expect(page.getByRole('button',{name:'导入历史',exact:true})).toBeVisible();
  expect(calls.some(c=>c.path==='preview'||c.path==='apply')).toBe(false);
});

test('the normal status refresh removes the suggestion when the first demand is stored',async({page})=>{
  const {calls,api}=await fixture(page,0,true);await page.goto('/#/library');
  const suggestion=page.getByRole('button',{name:'导入历史',exact:true});
  await expect(suggestion).toBeVisible();
  // Eligibility is global, independent of the visible library rows or filter.
  await page.getByLabel('搜索曲库').fill('no matching song');
  await page.getByRole('button',{name:'查找',exact:true}).click();
  api.state.vrcxImportAvailable=false;
  await expect(suggestion).toHaveCount(0,{timeout:10000});
  await expect(page.getByRole('button',{name:'导入 VRCX 历史',exact:true})).toBeVisible();
  expect(calls.map(c=>c.path)).toEqual(['detect']);
});

test('failed dismissal stays visible and can be retried',async({page})=>{
  const {calls}=await fixture(page,0,true);let failing=true;
  await page.route('**/api/vrcx/dismiss',r=>failing?r.fulfill({status:500,body:'无法保存忽略选择，请重试'}):r.fallback());
  await page.goto('/#/library');await page.getByRole('button',{name:'忽略',exact:true}).click();
  await expect(page.locator('[aria-label="VRCX 历史导入建议"]').getByText('未能忽略提示，请重试。')).toBeVisible();
  await expect(page.getByRole('button',{name:'导入历史',exact:true})).toBeVisible();
  failing=false;await page.getByRole('button',{name:'忽略',exact:true}).click();
  await expect(page.getByRole('button',{name:'导入历史',exact:true})).toHaveCount(0);
  expect(calls.some(c=>c.path==='preview'||c.path==='apply')).toBe(false);
});

test('new statistics found on quick import go to the existing overwrite confirmation',async({page})=>{
  const {calls,report}=await fixture(page,0,true);await page.goto('/#/library');
  const action=page.getByRole('button',{name:'导入历史',exact:true});await expect(action).toBeVisible();
  report.existingSongs=3;
  await action.click();
  await expect(page.getByText(/已有 3 首歌曲的需求统计/)).toBeVisible();
  expect(calls.some(c=>c.path==='apply')).toBe(false);
  await page.getByRole('button',{name:'导入并覆盖…',exact:true}).click();
  await page.getByRole('button',{name:'取消',exact:true}).click();
  expect(calls.some(c=>c.path==='apply')).toBe(false);
  await page.getByRole('button',{name:'导入并覆盖…',exact:true}).click();
  await page.getByRole('button',{name:'强制导入并覆盖',exact:true}).click();
  await expect(page.getByText(/已导入 4 次历史需求/)).toBeVisible();
  expect(calls.filter(c=>c.path==='apply')).toEqual([{path:'apply',body:{id:'preview-1',overwrite:true}}]);
});

test('quick import conflict requires a new preview and never retries an overwrite',async({page})=>{
  const {calls}=await fixture(page,0,true);
  const attempts:unknown[]=[];
  await page.route('**/api/vrcx/apply',r=>{attempts.push(r.request().postDataJSON());return r.fulfill({status:409,body:'已有歌曲需求统计；请重新预览并明确确认整表覆盖'});});
  await page.goto('/#/library');await page.getByRole('button',{name:'导入历史',exact:true}).click();
  await expect(page.getByRole('button',{name:'读取并预览',exact:true})).toBeVisible();
  await expect(page.locator('#vrcx-import').getByText(/已有歌曲需求统计；请重新预览/)).toBeVisible();
  await expect(page.getByRole('button',{name:'导入并覆盖…',exact:true})).toHaveCount(0);
  expect(attempts).toEqual([{id:'preview-1',overwrite:false}]);
  expect(calls.filter(c=>c.path==='preview')).toHaveLength(1);
});

test('unconfirmed quick import keeps its preview token for an explicit idempotent retry',async({page})=>{
  const {calls,report}=await fixture(page,0,true);
  const attempts:unknown[]=[];
  await page.route('**/api/vrcx/apply',r=>{
    attempts.push(r.request().postDataJSON());
    return attempts.length===1?r.abort():r.fulfill({json:{...report,imported:true}});
  });
  await page.goto('/#/library');await page.getByRole('button',{name:'导入历史',exact:true}).click();
  await expect(page.getByRole('button',{name:'导入历史偏好',exact:true})).toBeVisible();
  await expect(page.locator('#vrcx-import').getByText(/导入结果尚未确认/)).toBeVisible();
  await page.getByRole('button',{name:'导入历史偏好',exact:true}).click();
  await expect(page.getByText(/已导入 4 次历史需求/)).toBeVisible();
  expect(attempts).toEqual([{id:'preview-1',overwrite:false},{id:'preview-1',overwrite:false}]);
  expect(calls.filter(c=>c.path==='preview')).toHaveLength(1);
});

test('missing or empty history after detection never writes statistics',async({page})=>{
  const {calls,report}=await fixture(page,0,true);
  await page.route('**/api/vrcx/preview',r=>r.fulfill({status:400,body:'找不到或无法读取所选 VRCX 数据库，请检查路径和读取权限'}));
  await page.goto('/#/library');await page.getByRole('button',{name:'导入历史',exact:true}).click();
  await expect(page.locator('[aria-label="VRCX 历史导入建议"]').getByText(/找不到或无法读取/)).toBeVisible();
  expect(calls.some(c=>c.path==='apply')).toBe(false);
  await page.unroute('**/api/vrcx/preview');
  Object.assign(report,{id:'',events:0,songs:0});
  await page.getByRole('button',{name:'导入历史',exact:true}).click();
  await expect(page.getByText('没有有效历史需求，原统计保持不变。')).toBeVisible();
  expect(calls.some(c=>c.path==='apply')).toBe(false);
});

test('VRCX keeps the selected database and preview across module jumps and page navigation',async({page})=>{
  const {calls}=await fixture(page,3);
  await page.goto('/#/library');
  const history=page.getByRole('button',{name:'导入 VRCX 历史',exact:true});
  await expect(history).toBeVisible();
  await expect.poll(()=>calls.map(c=>c.path)).toEqual(['detect']);
  await history.click();await enable(page);
  await page.getByRole('textbox',{name:'VRCX 数据库路径',exact:true}).click();
  const path=page.getByRole('textbox',{name:'VRCX 数据库路径',exact:true}), preview=page.getByText('4 次 / 2 首',{exact:true});
  await expect(path).toHaveValue('D:\\custom\\VRCX.sqlite3');
  await inspect(page);await expect(preview).toBeVisible();
  await page.getByRole('button',{name:'导入视频',exact:true}).click();await expect(preview).toBeVisible();
  await history.click();await expect(preview).toBeVisible();
  await page.evaluate(()=>{location.hash='/library?view=resources';});
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

test('restoring the default VRCX path discards the custom preview without importing',async({page})=>{
  const {calls}=await fixture(page);
  await page.goto('/#vrcx-import');
  const path=page.getByRole('textbox',{name:'VRCX 数据库路径',exact:true});
  const reset=page.getByRole('button',{name:'VRCX 数据库路径：恢复默认',exact:true});
  await expect(reset).toBeDisabled();
  await path.click();await expect(path).toHaveValue('D:\\custom\\VRCX.sqlite3');
  await inspect(page);await expect(page.getByText('4 次 / 2 首',{exact:true})).toBeVisible();
  await reset.click();await expect(path).toHaveValue('C:\\VRCX\\VRCX.sqlite3');
  await expect(page.getByText('4 次 / 2 首',{exact:true})).toHaveCount(0);
  expect(calls.some(c=>c.path==='apply')).toBe(false);
  await inspect(page);
  expect(calls.filter(c=>c.path==='preview').map(c=>c.body.path)).toEqual(['D:\\custom\\VRCX.sqlite3','']);
});

test('VRCX reads only on explicit preview and empty usage imports without overwrite',async({page})=>{
  const {calls}=await fixture(page);await page.goto('/#/library');await page.getByRole('button',{name:'导入 VRCX 历史',exact:true}).click();
  await expect(page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true})).toBeChecked();
  await expect(page.getByRole('textbox',{name:'VRCX 数据库路径',exact:true})).toHaveValue('C:\\VRCX\\VRCX.sqlite3');
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
  await page.getByRole('textbox',{name:'VRCX 数据库路径',exact:true}).click();
  await expect(page.getByRole('textbox',{name:'VRCX 数据库路径',exact:true})).toHaveValue('D:\\custom\\VRCX.sqlite3');
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
