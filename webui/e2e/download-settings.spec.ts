import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

test('download settings validate bounds, preserve zero and explain CPU cost',async({page},testInfo)=>{
  const api=await mockAPI(page);await page.goto('/#/settings');await page.getByRole('button',{name:'高级下载设置',exact:true}).click();
  const limit=page.getByRole('spinbutton',{name:'同时下载数量'});
  const interval=page.getByRole('spinbutton',{name:'并发增长间隔（毫秒）'});
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  await expect(limit).toHaveValue('16');await expect(interval).toHaveValue('250');
  await expect(page.getByText('为减小 CPU 占用，强烈不建议增加并发数或减小增长间隔。',{exact:true})).toBeVisible();
  await expect(limit).toHaveAccessibleDescription(/播放正在等待的缺块可临时突破此上限/);
  await expect(interval).toHaveAccessibleDescription(/0 表示关闭缓增/);
  for(const [input,values,reset] of [[limit,['','0','65','1.5'],'16'],[interval,['','-1','2001','0.5'],'250']] as const){
    for(const value of values){
      await input.fill(value);await save.click();
      expect(api.calls.filter(c=>c.path==='settings')).toHaveLength(0);
    }
    await input.fill(reset);
  }
  await limit.fill('64');await interval.fill('0');
  await navigate(page,'运行状态');await navigate(page,'设置');
  await expect(limit).toHaveValue('64');await expect(interval).toHaveValue('0');
  await save.click();await expect(page.getByRole('button',{name:'立即重启并应用'})).toBeEnabled();
  expect(api.state.settings).toMatchObject({downloadSoftLimit:64,downloadGrowIntervalMS:0});
  expect(api.state.activeSettings).toMatchObject({downloadSoftLimit:16,downloadGrowIntervalMS:250});
  await page.reload();await page.getByRole('button',{name:'高级下载设置',exact:true}).click();
  await expect(limit).toHaveValue('64');await expect(interval).toHaveValue('0');
  await limit.fill('1');await interval.fill('2000');await save.click();
  await expect(page.getByRole('button',{name:'立即重启并应用'})).toBeEnabled();
  expect(api.state.settings).toMatchObject({downloadSoftLimit:1,downloadGrowIntervalMS:2000});
  await page.setViewportSize({width:390,height:844});
  await limit.scrollIntoViewIfNeeded();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('download-settings-narrow.png')});
});

test('local protection grace preserves zero, validates bounds and waits for restart',async({page},testInfo)=>{
  const api=await mockAPI(page);await page.goto('/#/settings');await page.getByRole('button',{name:'高级存储设置',exact:true}).click();
  const grace=page.getByRole('spinbutton',{name:'本地延续保护时长（秒）'});
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  await expect(grace).toHaveValue('300');
  await expect(grace).toHaveAccessibleDescription(/最后一份需求结束后.*0 表示不延续保护/);
  for(const value of ['','-1','10001','0.5']){
    await grace.fill(value);await save.click();
    expect(api.calls.filter(c=>c.path==='settings')).toHaveLength(0);
  }
  await grace.fill('0');
  await navigate(page,'运行状态');await navigate(page,'设置');
  await expect(grace).toHaveValue('0');
  await save.click();await expect(page.getByRole('button',{name:'立即重启并应用'})).toBeEnabled();
  expect(api.state.settings.localProtectionGraceSeconds).toBe(0);
  expect(api.state.activeSettings.localProtectionGraceSeconds).toBe(300);
  await page.reload();await page.getByRole('button',{name:'高级存储设置',exact:true}).click();await expect(grace).toHaveValue('0');
  await grace.fill('10000');await save.click();
  await expect(save).toBeDisabled();
  expect(api.state.settings.localProtectionGraceSeconds).toBe(10000);
  expect(api.state.activeSettings.localProtectionGraceSeconds).toBe(300);
  await page.setViewportSize({width:390,height:844});
  await grace.scrollIntoViewIfNeeded();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('local-protection-settings-narrow.png')});
});
