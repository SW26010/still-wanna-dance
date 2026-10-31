import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

test('network follows the active backend, preserves diagnostics and releases the embedded page',async({page})=>{
  const api=await mockAPI(page);let loads=0;
  Object.assign(api.state.activeSettings,{upstreamMode:'proxy',proxyBackend:'mihomo'});
  api.state.proxyNodes={dashboard:true};
  // The saved settings already request direct mode, but have not been applied.
  api.state.restartRequired=true;
  await page.route('**/proxy/',route=>{loads++;return route.fulfill({contentType:'text/html',body:'<!doctype html><html lang="zh-CN"><title>原版面板夹具</title><body><button>节点夹具</button></body></html>'});});
  await page.goto('/#/settings');expect(loads).toBe(0);
  await navigate(page,'网络');
  const frame=page.locator('iframe[title="Mihomo 代理面板"]');
  await expect(page.frameLocator('iframe').getByRole('button',{name:'节点夹具'})).toBeVisible();
  await expect(page.getByRole('button',{name:'检查当前连接'})).toHaveCount(0);
  await expect(page.getByRole('navigation',{name:'网络功能',exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'开始连通性检测'})).toBeVisible();
  await expect(page.getByRole('button',{name:'开始下载速度测试'})).toBeVisible();
  await page.getByRole('button',{name:'开始连通性检测'}).click();
  await expect(page.getByRole('button',{name:'停止并放弃连通性检测'})).toBeEnabled();
  await expect(frame).toBeVisible();
  expect(loads).toBe(1);
  await page.getByRole('button',{name:'停止并放弃连通性检测'}).click();
  await page.frameLocator('iframe').getByRole('button',{name:'节点夹具'}).focus();
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});
  await expect(frame).toHaveCount(0);
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});
  await expect(page.frameLocator('iframe').getByRole('button',{name:'节点夹具'})).toBeVisible();
  await expect(page.locator('.proxy-dashboard')).toBeFocused();
  await navigate(page,'设置');await expect(frame).toHaveCount(0);
  // Once the saved direct mode becomes active, the ordinary network page returns.
  Object.assign(api.state.activeSettings,api.state.settings);api.state.restartRequired=false;
  await page.reload();await navigate(page,'网络');
  await expect(page.getByRole('button',{name:'检查当前连接'})).toBeVisible();
  await expect(frame).toHaveCount(0);
  expect(loads).toBe(2);
});

test('network bookmarks show diagnostics together with the dashboard without reloading it',async({page})=>{
  const api=await mockAPI(page);
  Object.assign(api.state.activeSettings,{upstreamMode:'proxy',proxyBackend:'mihomo'});
  api.state.proxyNodes={dashboard:true};
  await page.route('**/proxy/',route=>route.fulfill({contentType:'text/html',body:'<!doctype html><title>面板夹具</title><button>节点夹具</button>'}));
  const connections=page.locator('#network'),frame=page.locator('iframe');
  await page.goto('/#network');
  await expect(connections).toBeFocused();
  await expect(page.getByRole('button',{name:'检查当前连接',exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeVisible();
  await expect(frame).toBeVisible();
  await page.reload();
  await expect(connections).toBeFocused();
  await expect(frame).toBeVisible();
  await navigate(page,'网络');
  await expect(page).toHaveURL(/#\/network$/);
  await expect(page.frameLocator('iframe').getByRole('button',{name:'节点夹具'})).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/#network$/);
  await expect(connections).toBeFocused();
  await expect(frame).toBeVisible();
  await page.goForward();
  await expect(page).toHaveURL(/#\/network$/);
  await expect(frame).toBeVisible();
  await page.reload();
  await expect(page.frameLocator('iframe').getByRole('button',{name:'节点夹具'})).toBeVisible();
  await expect(connections).toBeVisible();
  await expect(page.getByRole('heading',{name:'下载速度测试',exact:true})).toBeVisible();
});

test('a Mihomo draft does not change the network page and an unavailable core has useful feedback',async({page})=>{
  const api=await mockAPI(page);
  Object.assign(api.state.settings,{upstreamMode:'proxy',proxyBackend:'mihomo'});
  await page.goto('/#/network');
  await expect(page.getByRole('button',{name:'检查当前连接'})).toBeVisible();
  await expect(page.locator('iframe')).toHaveCount(0);
  Object.assign(api.state.activeSettings,api.state.settings);
  api.state.proxyNodes={dashboard:false,activeError:'fixture core startup failure'};
  await page.reload();
  await expect(page.getByRole('region',{name:'代理面板',exact:true}).getByText('fixture core startup failure',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'开始下载速度测试',exact:true})).toBeVisible();
  await expect(page.locator('iframe')).toHaveCount(0);
});

test('a short viewport keeps the embedded panel inside the themed page and reachable by scrolling',async({page},info)=>{
  const api=await mockAPI(page);
  Object.assign(api.state.activeSettings,{upstreamMode:'proxy',proxyBackend:'mihomo'});
  api.state.proxyNodes={dashboard:true};
  await page.route('**/proxy/',route=>route.fulfill({contentType:'text/html',body:'<!doctype html><title>面板夹具</title><button>节点夹具</button>'}));
  await page.setViewportSize({width:320,height:180});await page.emulateMedia({colorScheme:'dark'});
  await page.goto('/#/network');
  await expect(page.frameLocator('iframe').getByRole('button',{name:'节点夹具'})).toBeVisible();
  const host=await page.locator('#root > .app-provider').boundingBox(),frame=await page.locator('iframe').boundingBox();
  expect(host!.y+host!.height).toBeGreaterThanOrEqual(frame!.y+frame!.height);
  await page.getByRole('button',{name:'开始连通性检测',exact:true}).scrollIntoViewIfNeeded();
  await page.locator('iframe').scrollIntoViewIfNeeded();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
  await page.screenshot({path:info.outputPath('short-dashboard.png'),fullPage:true,animations:'disabled'});
});
