import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigateSettings} from './navigation';

for(const [width,height] of [[1440,1000],[900,1200],[390,2000]])test(`short pages have no document overflow at ${width}x${height}`,async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width,height});await page.goto('/#/licenses');
  await expect(page.getByRole('tab',{name:'许可证与声明',exact:true})).toHaveAttribute('aria-selected','true');
  const metrics=await page.evaluate(()=>({viewport:innerHeight,document:document.documentElement.scrollHeight,content:document.querySelector('.page-content')!.getBoundingClientRect().bottom}));
  expect(metrics.content).toBeLessThan(height-16);
  expect(metrics.document).toBeLessThanOrEqual(metrics.viewport);
  await page.screenshot({path:info.outputPath('short-page.png'),animations:'disabled'});
  if(width>640){
    await page.getByRole('button',{name:'收起页面导航',exact:true}).click();
    await expect(page.getByRole('navigation',{name:'页面导航',exact:true})).toHaveCount(0);
    expect(await page.evaluate(()=>document.documentElement.scrollHeight)).toBeLessThanOrEqual(height);
  }
});

for(const width of [390,800,1440])test(`settings and network show their controls without nested disclosure at ${width}px`,async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width,height:1000});await page.goto('/#/settings');
  for(const tab of ['常规','存储','预加载与下载','网络']){
    await page.getByRole('tab',{name:tab,exact:true}).click();
    await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeVisible();
    await expect(page.locator('.fui-Accordion')).toHaveCount(0);
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.evaluate(()=>scrollTo(0,0));
    await page.screenshot({path:info.outputPath(tab+'.png'),fullPage:true,animations:'disabled'});
  }
  await page.getByRole('link',{name:'检测当前连接',exact:true}).click();
  await expect(page.getByRole('heading',{name:'网络',level:1,exact:true})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.evaluate(()=>scrollTo(0,0));
  await page.screenshot({path:info.outputPath('network.png'),fullPage:true,animations:'disabled'});
  await navigateSettings(page,'网络');
  await expect(page.getByRole('tab',{name:'网络',exact:true})).toBeFocused();
  await page.getByRole('link',{name:'检测当前连接',exact:true}).click();
  await expect(page.locator('#network-tests')).toBeFocused();
  await expect(page.getByRole('heading',{name:'连通性检测',exact:true})).toBeVisible();
  await expect(page.getByRole('heading',{name:'下载速度测试',exact:true})).toBeVisible();
});
for(const width of [390,1280])test(`saving settings does not displace fields at ${width}px`,async({page})=>{
  await mockAPI(page);await page.setViewportSize({width,height:900});await page.goto('/#/settings');
  const field=page.getByRole('checkbox',{name:'启动程序时自动开启视频加速',exact:true}),save=page.getByRole('button',{name:'保存设置',exact:true});
  await field.check();await expect(save).toBeEnabled();
  const y=()=>field.evaluate(el=>el.getBoundingClientRect().top+scrollY);const before=await y();
  await save.click();await expect(page.getByRole('button',{name:'重启并应用'})).toBeEnabled();
  expect(Math.abs(await y()-before)).toBeLessThan(2);
});
