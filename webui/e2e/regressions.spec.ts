import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('saved settings stay locked until a fresh status arrives, including failed refreshes',async({page})=>{
  const api=await mockAPI(page);let hold=false,fail=false,release=()=>{};
  const waiting=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/status',async r=>{if(hold)await waiting;if(fail)return r.fulfill({status:503,json:{error:'temporarily offline'}});await r.fulfill({json:api.state});});
  await page.clock.install();await page.goto('/#/settings');
  const ahead=page.getByRole('spinbutton',{name:/提前准备时间 M/}),queue=page.getByRole('spinbutton',{name:/队列预缓存数量/});
  await ahead.fill('30');hold=true;
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByText('正在核对已保存的设置…',{exact:true})).toBeVisible();
  await expect(ahead).toHaveValue('30');await expect(queue).toBeDisabled();
  await expect(page.getByRole('combobox',{name:'上游连接',exact:true})).toBeDisabled();
  fail=true;release();await expect(page.getByText('无法连接控制台，请检查程序是否仍在运行。')).toBeVisible();
  await expect(queue).toBeDisabled();await expect(ahead).toHaveValue('30');
  fail=false;await page.clock.fastForward(5100);await expect(queue).toBeEnabled();
  await expect(ahead).toHaveValue('30');await queue.fill('4');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='settings').length).toBe(2);
  expect(api.calls.filter(c=>c.path==='settings').at(-1)?.body).toMatchObject({prepareAheadSeconds:30,queuePrefetchCount:4});
});

test('terms hosts recovery waits for UAC and reports a timed out result as unconfirmed',async({page})=>{
  // Route AbortSignal timeout through the browser clock; no real two-minute wait.
  await page.addInitScript(()=>{AbortSignal.timeout=(ms:number)=>{const c=new AbortController();setTimeout(()=>c.abort(new DOMException('Timed out','TimeoutError')),ms);return c.signal;};});
  let release=()=>{};const waiting=new Promise<void>(resolve=>{release=resolve;});
  let calls=0;
  await page.route('**/api/hosts/disable',async r=>{calls++;await waiting;await r.fulfill({json:{ok:true}}).catch(()=>{});});
  await page.clock.install();await page.goto('/terms');
  const button=page.getByRole('button',{name:'恢复游戏直连'});
  await button.click();await expect.poll(()=>calls).toBe(1);await page.clock.fastForward(20000);
  await expect(button).toBeDisabled();await expect(page.getByText(/最多等待 120 秒/)).toBeVisible();
  release();await expect(page.getByText('hosts 恢复操作已完成。')).toBeVisible();
  await page.route('**/api/hosts/disable',()=>{});
  await button.click();await page.clock.fastForward(119000);await expect(button).toBeDisabled();
  await page.clock.fastForward(1001);
  await expect(page.getByText(/操作结果尚未确认.*勿重复提交/)).toBeVisible();await expect(button).toBeEnabled();
});

test('request group remains expanded and focused as its oldest request changes',async({page})=>{
  await mockAPI(page);let ids=[3,2];
  const record=(id:number)=>({id,at:1700000000000+id*1000,resource:'fixture-resource',method:'GET',range:'bytes=0-',elapsedMS:100,bytes:200,status:206,cache:'HIT',outcome:'completed',songs:[{id:'1',title:'分段歌曲'}]});
  await page.route('**/api/requests?*',r=>r.fulfill({json:{storageID:'fixture',requests:(new URL(r.request().url()).searchParams.get('limit')==='100'?[4,3,2,1]:ids).map(record),hasMore:true}}));
  await page.clock.install();await page.goto('/#/monitor');
  const group=page.getByRole('button',{name:'分段歌曲 · 请求 ×2',exact:true});await group.click();
  const element=await group.elementHandle();
  ids=[4,3];await page.clock.fastForward(5100);
  await expect(group).toHaveAttribute('aria-expanded','true');await expect(group).toBeFocused();
  expect(await element!.evaluate(el=>el.isConnected)).toBe(true);
  await page.getByRole('button',{name:'加载更早记录（增加 50 条）'}).click();
  await expect(page.getByRole('button',{name:'分段歌曲 · 请求 ×4',exact:true})).toHaveAttribute('aria-expanded','true');
  expect(await element!.evaluate(el=>el.isConnected)).toBe(true);
});
