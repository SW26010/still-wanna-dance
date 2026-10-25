import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

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

test('invalid fields stay visible and receive native validation focus',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#prepareAheadSeconds');


  const input=page.getByRole('spinbutton',{name:'同时下载数量',exact:true});
  await input.fill('0');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();

  await expect(input).toBeFocused();
  expect(api.calls.some(call=>call.path==='settings')).toBe(false);
  await input.fill('16');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
});

test('help contents locate questions without requiring a status connection',async({page})=>{
  await mockAPI(page);
  await page.route('**/api/status',r=>r.fulfill({status:503,body:'offline'}));
  await page.goto('/help');
  const toc=page.getByRole('navigation',{name:'帮助目录',exact:true});
  for(const [name,id] of [['播放失败','help-playback'],['空间不足与视频保留','help-storage'],['停止与卸载','help-stop'],['日志与当前路径','help-paths']]){
    await toc.getByRole('link',{name,exact:true}).click();
    await expect(page.locator('#'+id)).toBeFocused();
    await expect(page.locator('#'+id)).toBeInViewport();
  }
  await page.reload();
  await expect(page.locator('#help-paths')).toBeFocused();
  await navigate(page,'关于');
  await expect.poll(()=>page.evaluate(()=>scrollY)).toBe(0);
});

test('exit restoration changes only after saving and applies to this exit',async({page},info)=>{
  const api=await mockAPI(page);await page.setViewportSize({width:390,height:850});await page.goto('/#/settings');

  const restore=page.getByRole('checkbox',{name:'退出时恢复游戏直连',exact:true});
  await expect(restore).toBeChecked();await restore.uncheck();
  expect(api.calls.some(c=>c.path==='settings'||c.path.startsWith('hosts/'))).toBe(false);
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
  expect(api.state.settings.restoreHostsOnExit).toBe(false);expect(api.state.activeSettings.restoreHostsOnExit).toBe(false);
  await page.screenshot({path:info.outputPath('exit-policy.png'),fullPage:true});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.getByRole('button',{name:'退出程序',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('退出后保留游戏接入');
});

test('failed exit permission preparation keeps the draft and original exit policy',async({page})=>{
  const api=await mockAPI(page);
  api.state.settings.restoreHostsOnExit=false;api.state.activeSettings.restoreHostsOnExit=false;
  await page.route('**/api/settings',route=>route.fulfill({status:400,json:{error:'未保存设置：退出恢复权限授权已取消'}}));
  await page.goto('/#/settings');

  const restore=page.getByRole('checkbox',{name:'退出时恢复游戏直连',exact:true});
  await restore.check();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('listitem',{name:'未保存设置：退出恢复权限授权已取消',exact:true})).toBeVisible();
  await expect(restore).toBeChecked();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  await page.getByRole('button',{name:'退出程序',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('退出后保留游戏接入');
});
