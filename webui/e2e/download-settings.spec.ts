import {navigateSettings,navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('download settings validate bounds, preserve zero without recommendation hints',async({page},testInfo)=>{
  await page.setViewportSize({width:1440,height:1000});
  const api=await mockAPI(page);await page.goto('/#prepareAheadSeconds');
  const limit=page.getByRole('spinbutton',{name:'同时下载数量'});
  const interval=page.getByRole('spinbutton',{name:'并发增长间隔（毫秒）'});
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  await expect(limit).toHaveValue('16');await expect(interval).toHaveValue('250');
  const notice='通常无需调整，修改前请了解其影响。';
  await expect(page.getByText(notice,{exact:true})).toHaveCount(2);
  for(const name of ['下载','缓存'])await expect(page.getByRole('group',{name,exact:true}).getByText(notice,{exact:true})).toBeVisible();
  await expect(page.getByRole('group',{name:'队列预加载',exact:true}).getByText(notice,{exact:true})).toHaveCount(0);
  for(const width of [1440,320]){
  await page.setViewportSize({width,height:1000});
  for(const [label,name,description] of [
    ['同时下载数量','同时下载数量','普通下载的并发上限；播放正在等待的数据可突破此限制。'],
    ['并发增长间隔','并发增长间隔（毫秒）','有下载积压时，每隔此时间最多增加一路并发；设为 0 时按需直接使用并发上限。'],
    ['提前准备时间','提前准备时间（秒）','提前为预计即将使用的视频数据安排下载。'],
    ['延续保护时间','延续保护时间（秒）','需求结束后继续保护缓存的时间；设为 0 时不延续保护。'],
  ]){
    const input=page.getByRole('spinbutton',{name,exact:true});
    await expect(input).toHaveAccessibleDescription(description);
    await expect(page.getByRole('tooltip').filter({hasText:description})).toHaveCount(0);
    await page.getByText(label,{exact:true}).hover();
    await expect(page.getByRole('tooltip')).toHaveText(description);
    await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toBeHidden();
    await page.mouse.move(0,0);
    await input.hover();
    await expect(page.getByRole('tooltip')).toHaveText(description);
    const controlBox=(await input.boundingBox())!,tooltipBox=(await page.getByRole('tooltip').boundingBox())!;
    expect(tooltipBox.x,'tooltip stays beside the hovered field').toBeLessThan(controlBox.x+controlBox.width);
    expect(tooltipBox.x+tooltipBox.width).toBeGreaterThan(controlBox.x);
    if(label==='同时下载数量')await page.screenshot({path:testInfo.outputPath(`download-tooltip-${width}.png`)});
    await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toBeHidden();
    await page.mouse.move(0,0);await input.focus();
    await expect(page.getByRole('tooltip')).toHaveText(description);
    await page.keyboard.press('Escape');await input.blur();
  }
  }
  await page.setViewportSize({width:1440,height:1000});
  for(const [input,values,reset] of [[limit,['0','65','1.5'],'16'],[interval,['-1','2001','0.5'],'250']] as const){
    for(const value of values){
      await input.fill(value);await save.click();
      expect(api.calls.filter(c=>c.path==='settings')).toHaveLength(0);
    }
    await input.fill(reset);
  }
  await limit.fill('64');await interval.fill('0');
  await navigateSettings(page,'网络');await navigateSettings(page,'下载与缓存');
  await expect(limit).toHaveValue('64');await expect(interval).toHaveValue('0');
  await save.click();await expect(page.getByRole('button',{name:'重启并应用'})).toBeEnabled();
  expect(api.state.settings).toMatchObject({downloadConcurrencySoftLimit:64,downloadGrowIntervalMS:0});
  expect(api.state.activeSettings).toMatchObject({downloadConcurrencySoftLimit:16,downloadGrowIntervalMS:250});
  await page.reload();
  await expect(limit).toHaveValue('64');await expect(interval).toHaveValue('0');
  await limit.fill('1');await interval.fill('2000');await save.click();
  await expect(page.getByRole('button',{name:'重启并应用'})).toBeEnabled();
  expect(api.state.settings).toMatchObject({downloadConcurrencySoftLimit:1,downloadGrowIntervalMS:2000});
  await page.setViewportSize({width:390,height:844});
  await limit.scrollIntoViewIfNeeded();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('download-settings-narrow.png')});
});

test('local protection grace preserves zero, validates bounds and waits for restart',async({page},testInfo)=>{
  const api=await mockAPI(page);await page.goto('/#download-settings');
  const grace=page.getByRole('spinbutton',{name:'延续保护时间（秒）'});
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  const estimate=page.getByRole('group',{name:'缓存',exact:true}).locator('legend .secondary');
  await expect(estimate).toHaveText('预计 190.73 MiB');
  await expect(grace).toHaveValue('300');
  await expect(grace).toHaveAccessibleDescription('需求结束后继续保护缓存的时间；设为 0 时不延续保护。');
  for(const value of ['-1','10001','0.5']){
    await grace.fill(value);await expect(estimate).toBeEmpty();await save.click();
    expect(api.calls.filter(c=>c.path==='settings')).toHaveLength(0);
  }
  await grace.fill('0');
  await expect(estimate).toHaveText('预计 119.21 MiB');
  await navigateSettings(page,'网络');await navigate(page,'曲库');await navigateSettings(page,'下载与缓存');
  await expect(grace).toHaveValue('0');
  await save.click();await expect(page.getByRole('button',{name:'重启并应用'})).toBeEnabled();
  expect(api.state.settings.localProtectionGraceSeconds).toBe(0);
  expect(api.state.activeSettings.localProtectionGraceSeconds).toBe(300);
  await page.reload();await expect(grace).toHaveValue('0');
  await grace.fill('10000');await expect(estimate).toHaveText('预计 2503.40 MiB');await save.click();
  await expect(save).toBeDisabled();
  expect(api.state.settings.localProtectionGraceSeconds).toBe(10000);
  expect(api.state.activeSettings.localProtectionGraceSeconds).toBe(300);
  await page.setViewportSize({width:390,height:844});
  await grace.scrollIntoViewIfNeeded();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('local-protection-settings-narrow.png')});
});


test('cleared download numbers show defaults and save defaults while preserving explicit zero',async({page},info)=>{
  const api=await mockAPI(page);
  const fields=[
    ['同时下载数量','downloadConcurrencySoftLimit',16,32],
    ['并发增长间隔（毫秒）','downloadGrowIntervalMS',250,0],
    ['提前准备时间（秒）','prepareAheadSeconds',500,1000],
    ['延续保护时间（秒）','localProtectionGraceSeconds',300,0],
  ] as const;
  for(const [,key,,saved] of fields)api.state.settings[key]=api.state.activeSettings[key]=saved;
  await page.goto('/#download-settings');
  for(const [name,,value] of fields){
    const input=page.getByRole('spinbutton',{name,exact:true});
    await input.fill('');await input.blur();
    await expect(input).toHaveValue('');
    await expect(input).toHaveAttribute('placeholder',String(value));
    await expect(input).not.toHaveAttribute('aria-invalid','true');
  }
  await navigateSettings(page,'常规');await navigateSettings(page,'下载与缓存');
  for(const [name] of fields)await expect(page.getByRole('spinbutton',{name,exact:true})).toHaveValue('');
  await expect(page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true})).toHaveAccessibleDescription('提前为预计即将使用的视频数据安排下载。');
  await expect(page.getByRole('group',{name:'缓存',exact:true}).locator('legend .secondary')).toHaveText('预计 190.73 MiB');
  await page.setViewportSize({width:390,height:1000});
  await page.locator('#download-settings').screenshot({path:info.outputPath('empty-defaults.png'),animations:'disabled'});
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  await save.click();await expect(save).toBeDisabled();
  expect(api.calls.filter(c=>c.path==='settings').at(-1)?.body).toEqual(Object.fromEntries(fields.map(([,key,value])=>[key,value])));
  await page.reload();
  for(const [name,key,value,saved] of fields){
    await expect(page.getByRole('spinbutton',{name,exact:true})).toHaveValue(String(value));
    expect(api.state.settings[key]).toBe(value);expect(api.state.activeSettings[key]).toBe(saved);
  }
  // Clearing a value already at its default still offers save and discard.
  const limit=page.getByRole('spinbutton',{name:'同时下载数量',exact:true});
  await limit.fill('');await expect(save).toBeEnabled();
  await page.getByRole('button',{name:'放弃更改',exact:true}).click();await expect(limit).toHaveValue('16');
  await limit.fill('');await save.click();await expect(save).toBeDisabled();await expect(limit).toHaveValue('16');
});
