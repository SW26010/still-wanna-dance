import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

for(const endpoint of ['inventory','downloads'] as const)for(const failure of ['503','timeout'] as const)test(`${endpoint} keeps following active work after ${failure} until completion`,async({page})=>{
  await mockAPI(page);
  let reads=0,fail=false,complete=false;
  await page.route('**/api/'+endpoint,route=>{
    reads++;
    if(fail)return failure==='503'?route.fulfill({status:503,json:{error:'temporarily unavailable'}}):route.abort('timedout');
    return route.fulfill({json:endpoint==='inventory'
      ?{totalSongs:2,coveredSongs:complete?2:1,coverageKnown:true,updated:complete?'2026-10-28T08:00:00Z':'2026-10-16T00:00:00Z',scanning:!complete}
      :{tasks:complete?[]:[{id:1,resource:'key1',host:'fixture',stage:'download',size:100,bytes:75,bytesPerSecond:10,idleMS:0}]}});
  });
  await page.clock.install();await page.goto('/#/library');
  const coverage=page.getByRole('group',{name:'曲库覆盖率',exact:true}),update=page.getByRole('button',{name:'更新曲库',exact:true});
  if(endpoint==='inventory'){await expect(coverage).toContainText('50.0%');await expect(update).toHaveAttribute('aria-busy','true');}
  else await expect(page.getByText('下载中 75.0%',{exact:true})).toBeVisible();
  fail=true;await page.clock.fastForward(endpoint==='inventory'?1100:5100);await expect.poll(()=>reads).toBe(2);
  if(endpoint==='inventory')await expect(update).toBeEnabled();
  else await expect(page.getByRole('listitem').filter({hasText:'下载进度暂时无法读取。'})).toBeVisible();
  await page.clock.fastForward(endpoint==='inventory'?1100:5100);await expect.poll(()=>reads).toBe(3);
  if(endpoint==='inventory')await expect(update).toBeEnabled();
  else await expect(page.getByRole('listitem').filter({hasText:'下载进度暂时无法读取。'})).toBeVisible();
  fail=false;complete=true;await page.clock.fastForward(endpoint==='inventory'?1100:5100);
  await expect.poll(()=>reads).toBe(4);
  if(endpoint==='inventory'){await expect(coverage).toContainText('100.0%');await expect(update).toHaveAttribute('aria-busy','false');}
  else await expect(page.getByText('下载中 75.0%',{exact:true})).toHaveCount(0);
  const completedReads=reads;await page.clock.fastForward(31000);expect(reads).toBe(completedReads);
});

test('an initial inventory failure does not start polling without observed active work',async({page})=>{
  await mockAPI(page);let reads=0;
  await page.route('**/api/inventory',route=>{reads++;return route.fulfill({status:503,json:{error:'unavailable'}});});
  await page.clock.install();await page.goto('/#/library');
  await expect.poll(()=>reads).toBe(1);await expect(page.getByRole('button',{name:'更新曲库',exact:true})).toBeEnabled();
  await page.clock.fastForward(31000);expect(reads).toBe(1);
});
