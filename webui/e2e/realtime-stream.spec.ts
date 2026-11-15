import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

for(const view of ['history','blocks'] as const){
  test(`${view} releases pagehide and pending retries, then resumes on pageshow`,async({page})=>{
    const api=await mockAPI(page);
    const streams=async()=>((await(await page.request.get('/__fixture/streams?id='+api.fixtureID)).json())[view]||0);
    await page.clock.install();await page.goto('/#/'+(view==='history'?'timeline':'home'));
    await expect.poll(streams).toBe(1);
    await page.evaluate(()=>dispatchEvent(new Event('pagehide')));
    await expect.poll(streams).toBe(0);
    await page.clock.fastForward(2100);expect(await streams()).toBe(0);
    await page.evaluate(()=>dispatchEvent(new Event('pageshow')));
    await expect.poll(streams).toBe(1);
    await page.request.post('/__fixture/disconnect',{data:{id:api.fixtureID,kind:view}});
    await expect.poll(streams).toBe(0);
    await page.evaluate(()=>dispatchEvent(new Event('pagehide')));
    await page.clock.fastForward(2100);expect(await streams()).toBe(0);
    await page.evaluate(()=>dispatchEvent(new Event('pageshow')));
    await expect.poll(streams).toBe(1);
    await page.request.post('/__fixture/disconnect',{data:{id:api.fixtureID,kind:view}});
    await navigate(page,'设置');await expect.poll(streams).toBe(0);
    await page.clock.fastForward(2100);expect(await streams()).toBe(0);
  });

  test(`${view} rejects malformed events and stops terminal subscriptions without retry`,async({page})=>{
    await mockAPI(page);let connections=0;
    await page.routeWebSocket('**/api/'+(view==='history'?'history':'block-demands')+'/events',socket=>{
      connections++;
      socket.send(connections===1?'malformed':JSON.stringify(view==='history'?{event:'history',data:{closed:true}}:{event:'closed',data:{}}));
    });
    await page.clock.install();await page.goto('/#/'+(view==='history'?'timeline':'home'));
    await expect.poll(()=>connections).toBe(1);
    if(view==='history')await expect(page.getByRole('listitem').getByText('历史更新无法读取，请重新查询。',{exact:true})).toBeVisible();
    await page.clock.fastForward(2100);expect(connections).toBe(1);
    await navigate(page,'设置');await navigate(page,view==='history'?'历史':'加速');
    await expect.poll(()=>connections).toBe(2);
    if(view==='history')await expect(page.getByRole('listitem').getByText('历史更新已停止。',{exact:true})).toBeVisible();
    await page.evaluate(()=>{dispatchEvent(new Event('pagehide'));dispatchEvent(new Event('pageshow'));});
    await page.clock.fastForward(2100);expect(connections).toBe(2);
  });
}
