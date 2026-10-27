import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('saved settings stay locked until a fresh status arrives, including failed refreshes',async({page})=>{
  const api=await mockAPI(page);let hold=false,fail=true,release=()=>{};
  const waiting=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/status',async r=>{if(hold)await waiting;if(fail)return r.fulfill({status:503,json:{error:'temporarily offline'}});await r.fulfill({json:api.state});});
  await page.clock.install();await page.goto('/#prepareAheadSeconds');
  const connection=page.getByRole('status',{name:'后台程序',exact:true});
  await expect(connection).toHaveText('后台程序：读取失败');
  fail=false;await page.clock.fastForward(5100);
  await expect(connection).toHaveText('后台程序：已连接');

  const ahead=page.getByRole('spinbutton',{name:/提前准备时间/}),retention=page.getByRole('spinbutton',{name:/并发增长间隔/});
  await ahead.fill('120');hold=true;
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByText('正在同步设置…',{exact:true})).toBeVisible();
  await expect(ahead).toHaveValue('120');await expect(retention).toBeDisabled();
  await expect(page.getByRole('checkbox',{name:'手动指定 VRChat 日志目录',exact:true})).toBeDisabled();
  fail=true;release();await expect(connection).toHaveText('后台程序：更新失败');await page.getByRole('button',{name:'后台状态'}).click();await expect(page.getByText('无法读取程序状态，请检查 SWD 是否仍在运行。')).toBeVisible();await page.keyboard.press('Escape');
  await expect(connection).toHaveText('后台程序：更新失败');
  await expect(retention).toBeDisabled();await expect(ahead).toHaveValue('120');
  fail=false;await page.clock.fastForward(5100);await expect(retention).toBeEnabled();
  await expect(connection).toHaveText('后台程序：已连接');
  await expect(ahead).toHaveValue('120');await retention.fill('4');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='settings').length).toBe(2);
  expect(api.calls.filter(c=>c.path==='settings').at(-1)?.body).toMatchObject({prepareAheadSeconds:120,downloadGrowIntervalMS:4});
});

test('acceleration waits for UAC and reports a timed out result as unconfirmed',async({page})=>{
  // The single acceleration action owns authorization.
  await mockAPI(page);
  // Route AbortSignal timeout through the browser clock; no real two-minute wait.
  await page.addInitScript(()=>{AbortSignal.timeout=(ms:number)=>{const c=new AbortController();setTimeout(()=>c.abort(new DOMException('Timed out','TimeoutError')),ms);return c.signal;};});
  let release=()=>{};const waiting=new Promise<void>(resolve=>{release=resolve;});
  let calls=0;
  await page.route('**/api/activation/enable',async r=>{calls++;await waiting;await r.fulfill({json:{ok:true}}).catch(()=>{});});
  await page.clock.install();await page.goto('/');
  const button=page.getByRole('button',{name:'开始加速'});
  await button.click();await expect.poll(()=>calls).toBe(1);await page.clock.fastForward(20000);
  await expect(button).toBeDisabled();await expect(page.getByRole('listitem',{name:/最多等待 120 秒/})).toBeVisible();
  release();await expect(page.getByRole('listitem',{name:'视频加速已开启。',exact:true})).toBeVisible();
  await page.route('**/api/activation/enable',()=>{});
  await button.click();await page.clock.fastForward(119000);await expect(button).toBeDisabled();
  await page.clock.fastForward(1001);
  await expect(page.getByRole('listitem',{name:/操作结果尚未确认.*勿重复提交/})).toBeVisible();await expect(button).toBeEnabled();
});

test('request detail keeps identity, expansion and focus while adjacent records change',async({page})=>{
  await mockAPI(page);let ids=[3,2];
  const record=(id:number)=>({id,at:Date.now(),resource:'fixture-resource',method:'GET',range:'bytes=0-',elapsedMS:100,bytes:200,status:206,cache:'HIT',outcome:'completed',songID:'42'});
  await page.route('**/api/history?*',r=>r.fulfill({json:{storageID:'fixture',requests:ids.map(record),hasMore:false,nextCursor:''}}));
  await page.clock.install();await page.goto('/#recent');
  const detail=page.getByRole('button',{name:'请求 3 的明细',exact:true});await detail.click();const element=await detail.elementHandle();
  ids=[4,3];await page.clock.fastForward(5100);
  await expect(detail).toHaveAttribute('aria-expanded','true');await expect(detail).toBeFocused();
  expect(await element!.evaluate(el=>el.isConnected)).toBe(true);
});


for(const outcome of ['approved','timeout'] as const)test('exit policy save waits for authorization: '+outcome,async({page})=>{
  const api=await mockAPI(page);
  api.state.settings.restoreHostsOnExit=false;api.state.activeSettings.restoreHostsOnExit=false;
  await page.addInitScript(()=>{AbortSignal.timeout=(ms:number)=>{const c=new AbortController();setTimeout(()=>c.abort(new DOMException('Timed out','TimeoutError')),ms);return c.signal;};});
  let release=()=>{},calls=0;const waiting=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/settings',async route=>{calls++;await waiting;if(outcome==='approved')await route.fallback();else await route.abort().catch(()=>{});});
  await page.clock.install();await page.goto('/#/settings');

  const restore=page.getByRole('checkbox',{name:'停止加速或退出时恢复游戏直连',exact:true}),save=page.getByRole('button',{name:'保存设置',exact:true});
  await restore.check();await save.click();await expect.poll(()=>calls).toBe(1);
  await page.clock.fastForward(40000);
  await expect(save).toBeDisabled();await expect(restore).toBeDisabled();
  await expect(page.getByRole('listitem',{name:'正在准备退出恢复权限，请留意管理员授权。',exact:true})).toBeVisible();
  if(outcome==='approved'){
    release();await expect(page.getByRole('listitem',{name:'设置已保存。',exact:true})).toBeVisible();
    expect(api.state.activeSettings.restoreHostsOnExit).toBe(true);
  }else{
    await page.clock.fastForward(80001);
    await expect(page.getByRole('listitem',{name:/操作结果尚未确认.*勿重复提交/})).toBeVisible();
    await expect(restore).toBeChecked();expect(api.state.activeSettings.restoreHostsOnExit).toBe(false);release();
  }
  expect(calls).toBe(1);
});
