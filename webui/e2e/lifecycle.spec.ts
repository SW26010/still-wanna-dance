import {test,expect,type Page,type WebSocketRoute} from '@playwright/test';
import {mockAPI} from './api';

const badge=(page:Page)=>page.getByRole('status',{name:'后台程序',exact:true});
async function lifecycle(page:Page) {
  const sockets:WebSocketRoute[]=[];
  await page.routeWebSocket('**/api/lifecycle',socket=>{sockets.push(socket);socket.send(JSON.stringify({action:'running'}));});
  return sockets;
}

test('external exit reaches every page, including a hidden page, and survives failed polling',async({context})=>{
  const pages=[await context.newPage(),await context.newPage()];
  const connections=[];
  for(const page of pages){
    await mockAPI(page);connections.push(await lifecycle(page));
    await page.clock.install();await page.goto('/#/home');await expect(badge(page)).toHaveText('后台程序：已连接');
  }
  await pages[1].evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,get:()=>true});document.dispatchEvent(new Event('visibilitychange'));});
  for(const [index,page] of pages.entries()){
    for(const socket of connections[index])socket.send(JSON.stringify({action:'exit'}));
    await page.route('**/api/status',route=>route.abort('connectionrefused'));
    await page.clock.fastForward(5100);
    await expect(badge(page)).toHaveText('后台程序：未连接');
    await expect(page.getByRole('button',{name:'退出程序',exact:true})).toBeDisabled();
  }
});

test('unexpected notification disconnect keeps polling and reconnects on its next success',async({page})=>{
  const api=await mockAPI(page),sockets=await lifecycle(page);
  await page.clock.install();await page.goto('/#/home');await expect(badge(page)).toHaveText('后台程序：已连接');
  await expect.poll(()=>sockets.length).toBe(1);
  await sockets[0].close();
  await expect(badge(page)).toHaveText('后台程序：已连接');
  await page.route('**/api/status',route=>route.abort('connectionrefused'));await page.clock.fastForward(5100);
  await expect(badge(page)).toHaveText('后台程序：更新失败');expect(sockets).toHaveLength(1);
  await page.unroute('**/api/status');await page.clock.fastForward(5100);
  await expect(badge(page)).toHaveText('后台程序：已连接');await expect.poll(()=>sockets.length).toBe(2);
  expect(api.calls.some(call=>call.path==='exit'||call.path==='restart')).toBe(false);
});

test('accepted exit closes the existing diagnostic event streams',async({page})=>{
  const api=await mockAPI(page),sockets=await lifecycle(page);
  await page.goto('/#/network');await expect(badge(page)).toHaveText('后台程序：已连接');
  const streams=async()=>await (await page.request.get('/__fixture/streams?id='+api.fixtureID)).json();
  await expect.poll(streams).toMatchObject({connectivity:1,calibration:1});
  await expect.poll(()=>sockets.length).toBe(1);sockets[0].send(JSON.stringify({action:'exit'}));
  await expect(badge(page)).toHaveText('后台程序：未连接');
  await expect.poll(streams).toMatchObject({connectivity:0,calibration:0});
});

test('late status and action responses cannot overwrite an accepted external exit',async({page})=>{
  const api=await mockAPI(page),sockets=await lifecycle(page);
  let release!:()=>void;
  const held=new Promise<void>(resolve=>{release=resolve;});
  await page.clock.install();await page.goto('/#/home');await expect(badge(page)).toHaveText('后台程序：已连接');
  await expect.poll(()=>sockets.length).toBe(1);
  await page.route('**/api/status',async route=>{await held;await route.fulfill({json:api.state}).catch(()=>{});});
  await page.route('**/api/exit',async route=>{await held;await route.abort('connectionreset').catch(()=>{});});
  await page.clock.fastForward(5100);await page.getByRole('button',{name:'退出程序',exact:true}).click();
  sockets[0].send(JSON.stringify({action:'exit'}));
  await expect(badge(page)).toHaveText('后台程序：未连接');release();
  await expect(page.getByRole('listitem',{name:/操作结果尚未确认/})).toHaveCount(0);
  await page.clock.fastForward(5100);await expect(badge(page)).toHaveText('后台程序：未连接');
});

test('exit acknowledgement follows the winning restart and starts only one reconnect loop',async({page})=>{
  await mockAPI(page);const sockets=await lifecycle(page);let reloads=0;
  await page.route('**/api/exit',route=>route.fulfill({json:{ok:true,action:'restart'}}));
  await page.clock.install();await page.goto('/#/home');await expect(badge(page)).toHaveText('后台程序：已连接');
  await expect.poll(()=>sockets.length).toBe(1);
  await page.route('http://127.0.0.1:18473/',route=>{reloads++;return route.fulfill({contentType:'text/html',body:'<meta name="swd-token" content="browser-fixture-token">'});});
  await page.getByRole('button',{name:'退出程序',exact:true}).click();
  sockets[0].send(JSON.stringify({action:'restart'}));
  await expect(badge(page)).toHaveText('后台程序：未连接');
  await expect(page.getByRole('listitem',{name:'正在重启应用，连接恢复后自动刷新。',exact:true})).toBeVisible();
  await expect.poll(()=>reloads).toBe(1);
  await page.clock.fastForward(510);await expect.poll(()=>reloads).toBe(2);
});

test('a pending restart confirmation cannot submit after an external exit',async({page})=>{
  const api=await mockAPI(page),sockets=await lifecycle(page);
  api.state.import={running:true};api.state.restartRequired=true;
  await page.goto('/#preferences');await expect(badge(page)).toHaveText('后台程序：已连接');
  await page.getByRole('button',{name:'重启并应用',exact:true}).click();
  const dialog=page.getByRole('dialog',{name:'导入仍在运行',exact:true});
  await expect(dialog).toBeVisible();await expect.poll(()=>sockets.length).toBe(1);
  sockets[0].send(JSON.stringify({action:'exit'}));
  await expect(badge(page)).toHaveText('后台程序：未连接');
  await dialog.getByRole('button',{name:'确认',exact:true}).click();
  await expect(dialog).toHaveCount(0);
  expect(api.calls.filter(call=>call.path==='restart')).toHaveLength(0);
  await expect(page.getByRole('listitem',{name:'正在退出应用并处理游戏连接。',exact:true})).toBeVisible();
});
