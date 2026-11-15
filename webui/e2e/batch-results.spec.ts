import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';
import {expectWithClock} from './clock';

test('batch failure details stay paged and leave the DOM with the library',async({page})=>{
  const api=await mockAPI(page);
  const failures=Array.from({length:10575},(_,i)=>({id:i+1,name:'失败歌曲 '+(i+1),error:'上游不可用'}));
  api.state.batch={id:'first',total:failures.length,checked:failures.length,failed:failures.length};
  await page.route('**/api/batch/failures?*',route=>{
    const offset=Number(new URL(route.request().url()).searchParams.get('offset'));
    return route.fulfill({json:{offset,total:failures.length,failures:failures.slice(offset,offset+50)}});
  });
  await page.goto('/#/library');
  const table=page.getByRole('table',{name:'批量任务失败记录',exact:true});
  await expect(table.locator('tbody tr')).toHaveCount(50);
  await page.getByRole('button',{name:'下一页批量任务失败记录',exact:true}).click();
  await expect(table.locator('tbody tr').first()).toContainText('51 · 失败歌曲 51');
  await navigate(page,'加速');
  await expect(page.locator('table[aria-label="批量任务失败记录"]')).toHaveCount(0);
  await navigate(page,'曲库');
  await expect(table.locator('tbody tr')).toHaveCount(50);
});

test('a delayed failure page cannot replace a newer batch result',async({page})=>{
  await page.clock.install();
  const api=await mockAPI(page);
  api.state.batch={id:'first',failed:60};
  let release=()=>{},finished=()=>{},started=()=>{};
  const pending=new Promise<void>(resolve=>{release=resolve;}),settled=new Promise<void>(resolve=>{finished=resolve;}),requested=new Promise<void>(resolve=>{started=resolve;});
  await page.route('**/api/batch/failures?*',async route=>{
    const query=new URL(route.request().url()).searchParams,offset=Number(query.get('offset'));
    if(offset===50){started();await pending;}
    const newer=query.get('id')==='second';
    await route.fulfill({json:{offset,total:newer?1:60,failures:[{id:newer?99:1,name:newer?'新任务歌曲':'旧任务歌曲',error:'上游不可用'}]}});
    if(offset===50)finished();
  });
  await page.goto('/#/library');
  const table=page.getByRole('table',{name:'批量任务失败记录',exact:true});
  await expect(table).toContainText('旧任务歌曲');
  await page.getByRole('button',{name:'下一页批量任务失败记录',exact:true}).click();
  await requested;
  api.state.batch={id:'second',failed:1};
  await expectWithClock(page,()=>table.textContent()).toContain('新任务歌曲');
  release();await settled;
  await expect(table).toContainText('新任务歌曲');
  await expect(table).not.toContainText('旧任务歌曲');
});
