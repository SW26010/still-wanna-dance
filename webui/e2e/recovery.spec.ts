import {navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('removed page aliases use the unknown-route default',async({page})=>{
  await mockAPI(page);
  for(const hash of ['#/upstream','#/monitor','#/cache','#cache','#song-preferences']){
    await page.goto('/'+hash);
    await expect(page.getByRole('heading',{level:1,name:'加速',exact:true})).toBeVisible();
  }
});

test('settings anchors focus after initial reads and within the same page',async({page})=>{
  await mockAPI(page);await page.goto('/#prepareAheadSeconds');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间/})).toBeFocused();
  await page.evaluate(()=>{location.hash='maxCacheGiB';});
  await expect(page.locator('#maxCacheGiB')).toBeFocused();
  await page.evaluate(()=>{location.hash='recent';});await expect(page.locator('#recent')).toBeFocused();
});

test('network routes and section links open diagnostics while proxy links open settings',async({page})=>{
  const api=await mockAPI(page);
  for(const hash of ['#/network','#network','#network-tests','#network-access','#service']){
    await page.goto('/'+hash);
    await expect(page.getByRole('heading',{level:1,name:'网络',exact:true})).toBeVisible();
    await expect(page.getByRole('button',{name:'检查当前连接',exact:true})).toBeVisible();
    await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeVisible();
    if(!hash.startsWith('#/'))await expect(page.locator(hash)).toBeFocused();
    await expect(page.getByRole('combobox',{name:'上游连接',exact:true})).not.toBeVisible();
  }
  for(const hash of ['#network-proxy','#proxy-settings']){
    await page.goto('/'+hash);
    await expect(page.getByRole('heading',{level:1,name:'设置',exact:true})).toBeVisible();
    await expect(page.getByRole('tab',{name:'网络',exact:true})).toHaveAttribute('aria-selected','true');
    await expect(page.getByRole('combobox',{name:'上游连接',exact:true})).toBeVisible();
    await expect(page.locator(hash)).toBeFocused();
    await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toHaveCount(0);
  }
  expect(api.calls.some(c=>c.path==='settings'||c.path.includes('/start')||c.path.includes('/check'))).toBe(false);
});

test('Mihomo preview stays a draft and clearing it releases only that preview',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#network-proxy');
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  await page.getByRole('textbox',{name:'粘贴节点内容',exact:true}).fill('fixture-node');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await page.route('**/api/proxy/import',r=>r.fulfill({json:{id:'preview-1',accepted:1,skipped:0,failed:0,items:[{index:1,status:'accepted',protocol:'ss'}]}}));
  await page.getByRole('button',{name:'检查并导入到草稿'}).click();
  await expect(page.getByText(/接受 1 个/)).toBeVisible();expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await page.getByRole('button',{name:'清除本次导入草稿'}).click();
  await expect.poll(()=>api.calls.find(c=>c.path==='proxy/import/discard')?.body).toEqual({id:'preview-1'});
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('external-socks5');
  await page.getByRole('textbox',{name:/SOCKS5 地址/}).fill('127.0.0.1:7891');
  await page.getByRole('textbox',{name:'SOCKS5 用户名（可选）',exact:true}).fill('fixture-user');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='settings').length).toBe(1);
  expect(api.calls.find(c=>c.path==='settings')?.body).not.toHaveProperty('socks5Password');
});

test('history clears failed reads and recovers focus after a row disappears',async({page})=>{
  const api=await mockAPI(page);let failed=false;
  const rows=[2,1].map(id=>({id,at:1700000000000+id*1000,resource:'fixture-resource',method:'GET',range:'bytes=0-',elapsedMS:100,bytes:200,status:206,cache:'HIT',outcome:'completed',songs:[{id:'1',title:'分段歌曲'}]}));
  await page.route('**/api/history?*',r=>failed?r.fulfill({status:500,json:{error:'unavailable'}}):r.fulfill({json:{storageID:'fixture',requests:rows,hasMore:false}}));
  await page.goto('/#recent');
  const group=page.getByRole('button',{name:'请求 2 的明细',exact:true});await group.click();
  await expect(page.getByText('请求处理完成',{exact:true})).toHaveCount(2);
  failed=true;await api.historyChanged();
  await expect(page.getByRole('listitem',{name:'历史读取失败，请重新查询。',exact:true})).toBeVisible();await expect(group).toHaveCount(0);await expect(page.getByLabel('历史查询结果',{exact:true})).toBeFocused();
});

test('rejected cancellation stays running and stale SSE events cannot replace newer state',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/network');
  await page.getByRole('button',{name:'开始连通性检测'}).click();
  await expect(page.getByRole('button',{name:'停止并放弃连通性检测'})).toBeEnabled();
  await page.route('**/api/network/connectivity/cancel',r=>r.fulfill({status:503,json:{error:'取消未送达'}}));
  await page.getByRole('button',{name:'停止并放弃连通性检测'}).click();
  await expect(page.getByText(/操作未确认.*取消未送达/)).toBeVisible();
  await expect(page.getByRole('button',{name:'停止并放弃连通性检测'})).toBeEnabled();
  Object.assign(api.diagnostics.connectivity,{revision:1,run:0,running:false,phase:'idle'});await api.emit('connectivity');
  await expect(page.getByRole('button',{name:'停止并放弃连通性检测'})).toBeEnabled();
});

test('cache failures preserve rows but disable stale actions and retry reads no unrelated services',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/library?view=resources');await expect(page.getByRole('checkbox',{name:/测试歌曲 1/})).toBeEnabled();
  await page.route('**/api/library?*',r=>r.fulfill({status:503,json:{error:'busy'}}));
  await page.getByRole('button',{name:'检查本地视频',exact:true}).click();await expect(page.getByText(/曲库读取失败/)).toBeVisible();
  await expect(page.getByRole('checkbox',{name:/测试歌曲 1/})).toBeDisabled();
  expect(api.calls.some(c=>c.path==='cache/delete'||c.path==='inventory/scan'||c.path==='start')).toBe(false);
});

test('320px forced-colors layout keeps every console page usable',async({page})=>{
  await mockAPI(page);await page.setViewportSize({width:320,height:800});await page.emulateMedia({forcedColors:'active',reducedMotion:'reduce'});await page.goto('/');
  for(const name of ['加速','历史','曲库','网络','设置','关于']){
    await navigate(page,name);
    await page.getByRole('button',{name:'展开页面导航',exact:true}).click();
    await expect(page.getByRole('navigation',{name:'页面导航',exact:true}).getByRole('link',{name,exact:true})).toHaveAttribute('aria-current','page');
    await page.keyboard.press('Escape');
    await expect(page.getByRole('button',{name:'展开页面导航',exact:true})).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  }
  await navigate(page,'网络');
  await expect(page.getByRole('button',{name:'开始下载速度测试'})).toBeEnabled();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
