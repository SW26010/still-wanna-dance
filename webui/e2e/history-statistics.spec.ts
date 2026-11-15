import {test,expect} from '@playwright/test';
import {mockAPI,statisticsFixture} from './api';
import {navigate} from './navigation';

const visibility=async(page:import('@playwright/test').Page,hidden:boolean)=>page.evaluate(hidden=>{Object.defineProperty(document,'hidden',{configurable:true,value:hidden});document.dispatchEvent(new Event('visibilitychange'));},hidden);

test('statistics use history triggers and concise explanations and sample tooltips without refresh controls or timers',async({page},info)=>{
  const api=await mockAPI(page);await page.clock.install();
  let reads=0;
  await page.route('**/api/history/stats',route=>{
    reads++;
    return route.fulfill({json:statisticsFixture({sourceSamples:181,sources:{downloadBytes:1,cacheBytes:0,localBytes:0},ready:{samples:5,p50MS:reads,p95MS:9}})});
  });
  await page.goto('/#/timeline');
  const source=page.getByRole('heading',{name:'响应来源',exact:true});
  await expect(page.getByText('P50 1 ms · P95 9 ms',{exact:true})).toBeVisible();expect(reads).toBe(1);
  await expect(page.getByRole('button',{name:/刷新来源统计|刷新响应时间统计/})).toHaveCount(0);
  for(const action of ['hover','focus'] as const){
    await source[action]();await expect(page.getByRole('tooltip')).toHaveText('视频响应中，下载、缓存和本地视频各自所占的比例。');if(action==='focus')await page.screenshot({path:info.outputPath('source-explanation.png'),animations:'disabled'});await page.keyboard.press('Escape');await page.mouse.move(0,0);
  }
  const group=page.getByText('缓存／本地',{exact:true}).locator('..');
  await group.focus();await expect(page.getByRole('tooltip')).toHaveText('样本 5 个');await page.screenshot({path:info.outputPath('latency-samples.png'),animations:'disabled'});await page.keyboard.press('Escape');
  await page.clock.fastForward(121000);expect(reads).toBe(1);
  await api.historyChanged();await expect(page.getByText('P50 2 ms · P95 9 ms',{exact:true})).toBeVisible();expect(reads).toBe(2);
  await page.getByRole('button',{name:'查询历史',exact:true}).click();await expect.poll(()=>reads).toBe(3);
  await visibility(page,true);await api.historyChanged();await page.clock.fastForward(121000);expect(reads).toBe(3);
  await visibility(page,false);await expect.poll(()=>reads).toBe(4);
  await navigate(page,'设置');await page.clock.fastForward(121000);expect(reads).toBe(4);
  await navigate(page,'历史');await expect.poll(()=>reads).toBe(5);
  // A stable historical page follows manual queries and visibility, with no live subscription.
  await page.getByRole('button',{name:'按收到时间排序',exact:true}).click();await expect.poll(()=>reads).toBe(6);
  await page.clock.fastForward(121000);await api.historyChanged();expect(reads).toBe(6);
  await navigate(page,'设置');await navigate(page,'历史');await expect.poll(()=>reads).toBe(7);
});

test('statistics coalesce while a read is pending and leave the history list responsive',async({page})=>{
  const api=await mockAPI(page);let historyReads=0,statisticsReads=0,release!:()=>void;
  const pending=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/history?*',async route=>{historyReads++;await route.fallback();});
  await page.route('**/api/history/stats',async route=>{
    const read=++statisticsReads;if(read===2)await pending;
    await route.fulfill({json:statisticsFixture({ready:{samples:read,p50MS:read,p95MS:read}})});
  });
  await page.goto('/#/timeline');await expect(page.getByText('P50 1 ms · P95 1 ms',{exact:true})).toBeVisible();
  await api.historyChanged();await expect.poll(()=>statisticsReads).toBe(2);
  const before=historyReads;
  for(let i=0;i<4;i++)await api.historyChanged();
  await expect.poll(()=>historyReads).toBeGreaterThan(before);expect(statisticsReads).toBe(2);
  await expect(page.getByRole('button',{name:'查询历史',exact:true})).toBeEnabled();
  await expect(page.getByText('P50 1 ms · P95 1 ms',{exact:true})).toBeVisible();
  release();await expect(page.getByText('P50 3 ms · P95 3 ms',{exact:true})).toBeVisible();expect(statisticsReads).toBe(3);
});
