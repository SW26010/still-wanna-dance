import {navigateSettings,navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('live progress belongs to acceleration and manual work belongs to library',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/downloads',r=>r.fulfill({json:{bytesPerSecond:12000,tasks:[{id:'1',resource:'song',songs:[{id:'1',title:'正在准备的歌曲'}],stage:'download',bytes:100,size:1000,bytesPerSecond:12000,idleMS:0}]}}));
  await page.goto('/#/home');
  await expect(page.getByRole('heading',{level:1,name:'加速',exact:true})).toBeVisible();
  await expect(page.getByText('正在准备的歌曲',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'展开时间轴',exact:true})).toBeVisible();
  await navigate(page,'曲库');await expect(page.getByRole('button',{name:'下载缺失视频',exact:true})).toBeVisible();
  expect(api.calls.some(c=>c.path==='queue/start')).toBe(false);
});

test('video import, storage and coverage are found from local videos',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/cache');
  const coverage=page.getByText('曲库覆盖率',{exact:true});
  await expect(coverage).toBeVisible();
  expect((await coverage.boundingBox())!.y).toBeLessThan((await page.getByRole('table',{name:'曲库资源'}).boundingBox())!.y);
  await page.getByRole('button',{name:'导入视频',exact:true}).click();
  await page.getByRole('textbox',{name:'外部目录',exact:true}).fill('D:\\Videos');
  await page.getByRole('button',{name:'扫描并导入',exact:true}).click();
  expect(api.calls.find(c=>c.path==='import/start')?.body.source).toBe('D:\\Videos');
  await page.getByRole('button',{name:'返回曲库',exact:true}).click(); await page.getByRole('link',{name:'存储设置',exact:true}).click();
  await expect(page.getByRole('radio',{name:'不保留',exact:true})).toBeVisible();
});

test('separate settings locations save only their own draft and preserve the others',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#prepareAheadSeconds');
  await page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true}).fill('777');
  await page.getByRole('tab',{name:'常规',exact:true}).click();await page.getByRole('tab',{name:'预加载与下载',exact:true}).click();
  await expect(page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true})).toHaveValue('777');
  await navigate(page,'曲库');
  await page.getByRole('link',{name:'存储设置',exact:true}).click();
  await page.getByRole('radio',{name:'限制为',exact:true}).check();
  await page.getByRole('spinbutton',{name:/视频存储预算/}).fill('20');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
  await expect(page.getByRole('region',{name:'设置操作',exact:true}).getByText('其他页面仍有未保存配置，请保存或放弃后再重启。')).toBeVisible();
  expect(api.state.settings.prepareAheadSeconds).not.toBe(777);
  await navigateSettings(page,'预加载与下载');
  await expect(page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true})).toHaveValue('777');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(api.state.settings.maxCacheBytes).toBe(20*2**30);
  expect(api.state.settings.prepareAheadSeconds).toBe(777);
});

for(const width of [390,1280])test(`sidebar matches tasks and auxiliary pages (${width})`,async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width,height:844});await page.goto('/');
  await expect(page.getByRole('heading',{level:1,name:'加速',exact:true})).toBeVisible();
  if(width<=640)await page.getByRole('button',{name:'展开页面导航',exact:true}).click();
  await expect(page.getByRole('navigation',{name:'页面导航',exact:true}).getByRole('link')).toHaveText(['加速','历史','曲库','网络','设置','关于']);
  if(width<=640)await page.keyboard.press('Escape');
  for(const name of ['历史','曲库','网络','设置','关于']){
    await navigate(page,name);
    await expect(page.getByRole('heading',{level:1,name,exact:true})).toBeVisible();
  }
  await navigate(page,'关于');
  await expect(page.getByRole('heading',{level:2,name:'Load failed? But I still wanna dance!',exact:true})).toBeVisible();
  await navigate(page,'帮助');
  await expect(page.getByRole('link',{name:'重新进入使用引导',exact:true})).toBeVisible();
  await page.screenshot({path:info.outputPath('navigation-help.png'),fullPage:true,animations:'disabled'});
});

test('proxy configuration and checks share the same unsaved draft without saving it',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#network-proxy');
  await expect(page.getByRole('combobox',{name:'上游连接',exact:true})).toBeVisible();
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('textbox',{name:'SOCKS5 地址',exact:true}).fill('127.0.0.1:7891');
  await expect(page.getByRole('heading',{name:'设置',level:1,exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'检查当前连接',exact:true})).not.toBeVisible();
  await expect(page.getByRole('button',{name:'修复并开启加速',exact:true})).not.toBeVisible();
  await page.getByRole('link',{name:'检测此连接配置',exact:true}).click();
  await expect(page.getByRole('heading',{name:'网络',level:1,exact:true})).toBeVisible();
  await expect(page.getByRole('combobox',{name:'上游连接',exact:true})).not.toBeVisible();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).not.toBeVisible();
  await expect(page.getByRole('combobox',{name:'检测上游',exact:true})).toHaveValue('proxy');
  await page.getByRole('button',{name:'开始连通性检测',exact:true}).click();
  const call=api.calls.find(c=>c.path==='setup/network/connectivity/start');
  expect(call?.body.proxy).toMatchObject({socks5Address:'127.0.0.1:7891',upstreamMode:'proxy'});
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await page.getByRole('link',{name:'网络设置',exact:true}).click();
  await expect(page.getByRole('heading',{name:'设置',level:1,exact:true})).toBeVisible();
  await expect(page.getByRole('textbox',{name:'SOCKS5 地址',exact:true})).toHaveValue('127.0.0.1:7891');
  await navigate(page,'网络');
  await page.getByRole('link',{name:'接入诊断',exact:true}).click();
  await expect(page.getByRole('button',{name:'修复并开启加速',exact:true})).toBeVisible();
  await navigate(page,'加速');
  await expect(page.getByRole('button',{name:'修复并开启加速',exact:true})).toHaveCount(0);
});

test('normal startup stays on acceleration even after a manual wizard was left active',async({page})=>{
  await mockAPI(page);
  await page.route('**/api/setup',r=>r.fulfill({json:{schemaVersion:1,state:'active',step:'proxy'}}));
  await page.goto('/');
  await expect(page.getByRole('heading',{level:1,name:'加速',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'下一步',exact:true})).toHaveCount(0);
});

test('acceleration summarizes recent failures and links to their owning page',async({page},info)=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;
  api.state.activation={phase:'waiting',firstRequest:new Date().toISOString()};
  await page.route('**/api/requests?*',r=>r.fulfill({json:{requests:[{id:1,at:Date.now(),status:502,outcome:'failed',method:'GET',bytes:0,elapsedMS:100,resource:'song',cache:'MISS'}]}}));
  await page.setViewportSize({width:390,height:844});await page.goto('/');
  await expect(page.getByText(/最近请求：请求失败 · HTTP 502/)).toBeVisible();
  await expect(page.getByRole('link',{name:'检查连接',exact:true})).toBeVisible();
  await page.screenshot({path:info.outputPath('acceleration.png'),fullPage:true,animations:'disabled'});
  await page.getByRole('link',{name:'检查连接',exact:true}).click();
  await expect(page.getByRole('heading',{name:'网络',level:1,exact:true})).toBeVisible();
  await navigate(page,'曲库');
  await page.screenshot({path:info.outputPath('downloads.png'),fullPage:true,animations:'disabled'});
  await navigate(page,'曲库');
  await page.screenshot({path:info.outputPath('videos.png'),fullPage:true,animations:'disabled'});
});
