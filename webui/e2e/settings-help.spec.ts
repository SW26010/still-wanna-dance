import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

for(const width of [320,1280])test(`unsaved changes explain blocked restart without moving toolbar (${width})`,async({page},info)=>{
  const api=await mockAPI(page);
  Object.assign(api.state.settings,{logLevel:'debug',autoAccelerate:true});
  api.state.restartRequired=true;
  await page.setViewportSize({width,height:800});
  await page.goto('/#/settings');
  const actions=page.getByRole('region',{name:'设置操作',exact:true});
  const restart=actions.getByRole('button',{name:'重启并应用',exact:true});
  const before=await actions.boundingBox(),buttonBefore=await restart.boundingBox();
  await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
  await page.getByRole('spinbutton',{name:'同时下载数量',exact:true}).fill('17');
  await page.getByRole('tab',{name:'常规',exact:true}).click();
  await expect(restart).toBeDisabled();
  // Tab focus can scroll a narrow page; compare both states at the same offset.
  await page.evaluate(()=>scrollTo({top:0,behavior:'instant'}));
  expect(await actions.boundingBox()).toEqual(before);
  expect(await restart.boundingBox()).toEqual(buttonBefore);
  await restart.hover();
  await expect(page.getByRole('tooltip')).toHaveText('其他页面仍有未保存配置，请保存或放弃后再重启。');
  await page.keyboard.press('Escape');await page.mouse.move(0,0);
  await restart.focus();await expect(page.getByRole('tooltip')).toContainText('未保存配置');
  await page.keyboard.press('Enter');
  expect(api.calls.some(call=>call.path==='restart')).toBe(false);
  await page.screenshot({path:info.outputPath('blocked-restart.png')});
  await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
  await page.getByRole('button',{name:'放弃更改',exact:true}).click();
  await page.getByRole('tab',{name:'常规',exact:true}).click();
  await expect(restart).toBeEnabled();await restart.hover();
  await expect(page.getByRole('tooltip')).toContainText('管理员授权');
});

for(const width of [320,390,1280])test(`settings actions stay visible while editing and saving (${width})`,async({page},info)=>{
  const api=await mockAPI(page);
  await page.setViewportSize({width,height:800});
  await page.emulateMedia({colorScheme:width===390?'dark':'light'});
  await page.goto('/#prepareAheadSeconds');
  const actions=page.getByRole('region',{name:'设置操作',exact:true});
  await page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true}).fill('777');
  await expect(actions).toBeInViewport();
  const bar=await actions.boundingBox(),field=await page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true}).boundingBox();
  expect(bar!.y).toBeGreaterThanOrEqual(0);
  expect(field!.y).toBeGreaterThanOrEqual(bar!.y+bar!.height);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('settings-actions.png')});
  await actions.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(actions.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(api.state.settings.prepareAheadSeconds).toBe(777);
  await page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true}).fill('888');
  await actions.getByRole('button',{name:'放弃更改',exact:true}).click();
  await expect(page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true})).toHaveValue('777');
});

test('invalid fields stay visible and receive validation focus without a native bubble',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#prepareAheadSeconds');


  const input=page.getByRole('spinbutton',{name:'同时下载数量',exact:true});
  await input.fill('0');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();

  await expect(input).toBeFocused();
  expect(api.calls.some(call=>call.path==='settings')).toBe(false);
  await input.fill('16');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
});

test('help sections and direct anchors work without requiring a status connection',async({page})=>{
  await mockAPI(page);
  await page.route('**/api/status',r=>r.fulfill({status:503,body:'offline'}));
  await page.goto('/help');
  for(const name of ['开始使用','播放失败','空间不足与视频保留','停止与卸载','日志与当前路径']){
    await expect(page.getByRole('heading',{name,exact:true})).toBeVisible();
  }
  await page.goto('/#help-paths');
  await expect(page.locator('#help-paths')).toBeFocused();
  await expect(page.locator('#help-paths')).toBeInViewport();
  await page.reload();
  await expect(page.locator('#help-paths')).toBeFocused();
  await navigate(page,'关于');
  await expect.poll(()=>page.evaluate(()=>scrollY)).toBe(0);
});

test('exit restoration changes only after saving and applies to this exit',async({page},info)=>{
  const api=await mockAPI(page);await page.setViewportSize({width:390,height:850});await page.goto('/#/settings');

  const restore=page.getByRole('checkbox',{name:'停止加速或退出时恢复游戏直连',exact:true});
  await expect(restore).toBeChecked();await restore.uncheck();
  expect(api.calls.some(c=>c.path==='settings'||c.path.startsWith('hosts/'))).toBe(false);
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(restore).toBeEnabled();
  await expect(page.getByRole('region',{name:'设置操作',exact:true}).getByRole('status')).toHaveText('已生效');
  expect(api.state.settings).toEqual(api.state.activeSettings);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
  expect(api.state.settings.restoreHostsOnStopOrExit).toBe(false);expect(api.state.activeSettings.restoreHostsOnStopOrExit).toBe(false);
  await page.screenshot({path:info.outputPath('exit-policy.png'),fullPage:true});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.getByRole('button',{name:'退出程序',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('退出后保留游戏接入');
});

test('failed exit permission preparation keeps the draft and original exit policy',async({page})=>{
  const api=await mockAPI(page);
  api.state.settings.restoreHostsOnStopOrExit=false;api.state.activeSettings.restoreHostsOnStopOrExit=false;
  await page.route('**/api/settings',route=>route.fulfill({status:400,json:{error:'未保存设置：退出恢复权限授权已取消'}}));
  await page.goto('/#/settings');

  const restore=page.getByRole('checkbox',{name:'停止加速或退出时恢复游戏直连',exact:true});
  await restore.check();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('listitem',{name:'未保存设置：退出恢复权限授权已取消',exact:true})).toBeVisible();
  await expect(restore).toBeChecked();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  await page.getByRole('button',{name:'退出程序',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('退出后保留游戏接入');
});
