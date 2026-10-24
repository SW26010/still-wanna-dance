import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('five primary pages and legacy anchors reach their current owners',async({page})=>{
  await mockAPI(page);await page.goto('/#/settings');
  await expect(page.getByRole('navigation',{name:'页面导航'}).getByRole('link')).toHaveText(['视频加速','时间线','曲库','设置','关于']);
  for(const name of ['常规','存储','预加载与下载','网络'])await expect(page.getByRole('tablist',{name:'设置分类'}).getByRole('tab',{name,exact:true})).toBeVisible();
  for(const [hash,title,field] of [['#storage-settings','设置','优先保留收藏视频'],['#prepareAheadSeconds','设置','提前准备时间（秒）'],['#service','设置',''],['#cache','曲库',''],['#queue','视频加速',''],['#recent','时间线','']]){
    await page.evaluate(hash=>{location.hash=hash;},hash);
    await expect(page.getByRole('heading',{level:1,name:title,exact:true})).toBeVisible();
    if(field)await expect(page.getByLabel(field)).toBeVisible();
  }
  await page.evaluate(()=>{location.hash='#network-proxy';});
  await expect(page.getByLabel('上游连接',{exact:true})).toBeVisible();
  await expect(page.getByLabel('上游连接',{exact:true})).toHaveCount(1);
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

test('theme is immediate, browser-persistent and independent of startup settings',async({page})=>{
  const {calls}=await mockAPI(page);await page.emulateMedia({colorScheme:'light'});await page.goto('/#/settings');
  await page.getByLabel('主题',{exact:true}).selectOption('dark');
  await expect(page.locator('.app-provider')).toHaveAttribute('data-theme','dark');
  await page.reload();await expect(page.getByLabel('主题',{exact:true})).toHaveValue('dark');
  await page.getByLabel('主题',{exact:true}).selectOption('system');
  await expect(page.locator('.app-provider')).toHaveAttribute('data-theme','light');
  await page.emulateMedia({colorScheme:'dark'});await expect(page.locator('.app-provider')).toHaveAttribute('data-theme','dark');
  expect(calls.some(c=>c.path==='settings')).toBe(false);
});

test('about groups help, agreement and offline license links',async({page})=>{
  await mockAPI(page);await page.goto('/#/about');
  await page.getByRole('tab',{name:'许可证与声明'}).click();
  for(const [name,path] of [['软件许可证原文','software-license.txt'],['程序第三方声明原文','third-party-notices.txt'],['WebUI 第三方声明原文','webui-notices.txt']])await expect(page.getByRole('link',{name,exact:true})).toHaveAttribute('href','/assets/webui/'+path);
  await page.getByRole('tab',{name:'用户协议'}).click();await expect(page.getByRole('article',{name:'完整使用条款'})).toBeVisible();
  await page.getByRole('tab',{name:'使用帮助'}).click();await expect(page.getByRole('navigation',{name:'帮助目录'})).toBeVisible();
});
