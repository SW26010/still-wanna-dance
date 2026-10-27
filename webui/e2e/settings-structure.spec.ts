import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('six primary pages and section links reach their current owners',async({page})=>{
  await mockAPI(page);await page.goto('/#/settings');
  await expect(page.getByRole('navigation',{name:'页面导航'}).getByRole('link')).toHaveText(['加速','历史','曲库','网络','设置','关于']);
  for(const name of ['常规','存储','预加载与下载','网络'])await expect(page.getByRole('tablist',{name:'设置分类'}).getByRole('tab',{name,exact:true})).toBeVisible();
  for(const [hash,title,field] of [['#storage-settings','设置','优先保留收藏视频'],['#prepareAheadSeconds','设置','提前准备时间（秒）'],['#service','网络',''],['#/library?view=resources','曲库',''],['#queue','加速',''],['#recent','历史','']]){
    await page.evaluate(hash=>{location.hash=hash;},hash);
    await expect(page.getByRole('heading',{level:1,name:title,exact:true})).toBeVisible();
    if(field)await expect(page.getByLabel(field)).toBeVisible();
  }
  await page.evaluate(()=>{location.hash='#network-proxy';});
  await expect(page.getByLabel('上游连接',{exact:true})).toBeVisible();
  await expect(page.getByLabel('上游连接',{exact:true})).toHaveCount(1);
  await page.getByRole('tab',{name:'网络',exact:true}).click();
  await expect(page.getByRole('combobox',{name:'播放地址接口选择',exact:true})).toHaveCount(0);
  await expect(page.getByRole('spinbutton',{name:'自动测速间隔（分钟）',exact:true})).toBeVisible();
  await page.evaluate(()=>{location.hash='#help-paths';});
  await expect(page.getByRole('tab',{name:'使用帮助',exact:true})).toHaveAttribute('aria-selected','true');
  await expect(page.getByRole('heading',{name:'日志与当前路径',exact:true})).toBeVisible();
});

test('tab drafts survive and saves merge only the submitted section',async({page})=>{
  const {calls,state}=await mockAPI(page);await page.goto('/#prepareAheadSeconds');
  await page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true}).fill('900');
  await page.getByRole('tab',{name:'常规',exact:true}).click();
  await page.getByLabel('启动程序时自动打开 WebUI',{exact:true}).uncheck();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>calls.filter(c=>c.path==='settings').length).toBe(1);
  expect(state.settings.autoOpenWebUI).toBe(false);expect(state.settings.prepareAheadSeconds).toBe(500);
  await page.getByRole('tab',{name:'预加载与下载',exact:true}).click();
  await expect(page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true})).toHaveValue('900');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>calls.filter(c=>c.path==='settings').length).toBe(2);
  expect(state.settings.autoOpenWebUI).toBe(false);expect(state.settings.prepareAheadSeconds).toBe(900);
});

test('theme autosaves independently of drafts and survives reload',async({page})=>{
  const {calls,state}=await mockAPI(page);await page.emulateMedia({colorScheme:'light'});await page.goto('/#/settings');
  await page.getByLabel('启动程序时自动打开 WebUI',{exact:true}).uncheck();
  await page.getByLabel('主题',{exact:true}).selectOption('dark');
  await expect(page.locator('#root > .app-provider')).toHaveAttribute('data-theme','dark');
  await expect(page.locator('link[rel="icon"]')).toHaveAttribute('href','/favicon.ico');
  await expect.poll(()=>state.settings.theme).toBe('dark');
  expect(state.activeSettings.theme).toBe('dark');expect(state.restartRequired).toBe(false);
  expect(state.settings.autoOpenWebUI).toBe(true);
  await expect(page.getByLabel('启动程序时自动打开 WebUI',{exact:true})).not.toBeChecked();
  await page.getByRole('button',{name:'放弃更改',exact:true}).click();
  await expect(page.getByLabel('主题',{exact:true})).toHaveValue('dark');
  await page.reload();await expect(page.getByLabel('主题',{exact:true})).toHaveValue('dark');
  await page.getByLabel('主题',{exact:true}).selectOption('light');
  await expect(page.locator('#root > .app-provider')).toHaveAttribute('data-theme','light');
  await expect(page.locator('link[rel="icon"]')).toHaveAttribute('href','/favicon-light.ico');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await page.getByLabel('主题',{exact:true}).selectOption('system');
  await expect(page.locator('#root > .app-provider')).toHaveAttribute('data-theme','light');
  await page.emulateMedia({colorScheme:'dark'});await expect(page.locator('#root > .app-provider')).toHaveAttribute('data-theme','dark');
  expect(calls.some(c=>c.path==='settings')).toBe(false);
  expect(calls.filter(c=>c.path==='settings/theme').map(c=>c.body)).toEqual([{theme:'dark'},{theme:'light'},{theme:'system'}]);
});

test('theme failures preserve appearance and status polling follows program changes',async({page})=>{
  const {state}=await mockAPI(page);await page.emulateMedia({colorScheme:'light'});
  await page.addInitScript(()=>localStorage.setItem('swd-theme','dark'));
  await page.goto('/#/settings');
  await expect(page.locator('#root > .app-provider')).toHaveAttribute('data-theme','light');
  await page.route('**/api/settings/theme',route=>route.fulfill({status:400,body:'配置文件无法写入'}));
  await page.getByLabel('主题',{exact:true}).selectOption('dark');
  await expect(page.getByText('配置文件无法写入',{exact:true})).toBeVisible();
  await expect(page.getByLabel('主题',{exact:true})).toHaveValue('system');
  await expect(page.locator('#root > .app-provider')).toHaveAttribute('data-theme','light');
  expect(state.settings.theme).toBe('system');
  state.settings.theme=state.activeSettings.theme='dark';
  await expect(page.locator('#root > .app-provider')).toHaveAttribute('data-theme','dark',{timeout:10000});
  await expect(page.getByLabel('主题',{exact:true})).toHaveValue('dark');
});

test('fresh status corrects a delayed theme save after another client restores the previous preference',async({page})=>{
  const {state,calls}=await mockAPI(page);await page.emulateMedia({colorScheme:'light'});
  let saved=false,release!:()=>void;
  const held=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/settings/theme',async route=>{
    expect(route.request().postDataJSON()).toEqual({theme:'dark'});
    state.settings.theme=state.activeSettings.theme='dark';saved=true;
    await held;await route.fulfill({json:{ok:true}});
  });
  // Keep the intermediate dark value out of this page's status snapshots.
  await page.route('**/api/status',async route=>{if(saved)await held;await route.fallback();});
  await page.goto('/#/settings');
  const theme=page.getByLabel('主题',{exact:true});
  await expect(theme).toHaveValue('system');
  await theme.selectOption('dark');await expect.poll(()=>saved).toBe(true);
  // Another client saves system while this page still awaits its success response.
  state.settings.theme=state.activeSettings.theme='system';
  const reads=calls.filter(c=>c.path==='status').length;
  release();
  await expect.poll(()=>calls.filter(c=>c.path==='status').length).toBeGreaterThan(reads);
  await expect(theme).toBeEnabled();
  await expect(theme).toHaveValue('system');
  await expect(page.locator('#root > .app-provider')).toHaveAttribute('data-theme','light');
  await expect(page.locator('link[rel="icon"]')).toHaveAttribute('href','/favicon-light.ico');
});

test('about groups help, agreement and offline license links',async({page})=>{
  await mockAPI(page);await page.goto('/#/about');
  await page.getByRole('tab',{name:'许可证与声明'}).click();
  for(const [name,path] of [['软件许可证原文','software-license.txt'],['程序第三方声明原文','third-party-notices.txt'],['WebUI 第三方声明原文','webui-notices.txt']])await expect(page.getByRole('link',{name,exact:true})).toHaveAttribute('href','/assets/webui/'+path);
  await page.getByRole('tab',{name:'用户协议'}).click();await expect(page.getByRole('article',{name:'完整使用条款'})).toBeVisible();
  await page.getByRole('tab',{name:'使用帮助'}).click();await expect(page.getByRole('navigation',{name:'帮助目录'})).toBeVisible();
});
