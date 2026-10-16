import {navigateStep} from './navigation';
import { test, expect } from '@playwright/test';
import { mockAPI } from './api';

test('cache list keeps selection and secondary actions reachable by keyboard', async ({page}) => {
  const api = await mockAPI(page);
  await page.goto('/#/cache');
  const list = page.getByRole('grid', {name:'缓存文件'});
  await expect(list.getByRole('row')).toHaveCount(2);
  const first = list.getByRole('row').first(), second = list.getByRole('row').nth(1);
  const checkbox = first.getByRole('checkbox');
  await first.focus(); await page.keyboard.press('ArrowRight');
  await expect(checkbox).toBeFocused(); await page.keyboard.press('Space');
  await expect(checkbox).toBeChecked();
  await page.keyboard.press('ArrowRight');
  await expect(first.getByRole('button', {name:'资源与版本详情'})).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(first.getByText('资源指纹：key1')).toBeVisible();
  await page.keyboard.press('ArrowRight');
  await expect(first.getByRole('button', {name:'定位文件'})).toBeFocused();
  await page.keyboard.press('Escape'); await expect(first).toBeFocused();
  await page.keyboard.press('ArrowDown'); await expect(second).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(second.getByRole('button', {name:'资源与版本详情'})).toBeFocused();
  await expect(second.getByRole('checkbox')).toBeDisabled();
  await page.keyboard.press('Escape'); await expect(second).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('button', {name:'缓存效果统计'})).toBeFocused();
  await first.getByRole('button', {name:'定位文件'}).focus();
  await page.keyboard.press('Enter');
  await expect.poll(() => api.calls.find(c=>c.path==='cache/open')?.body).toEqual({storageID:'fixture',entries:[{key:'key1',stamp:'stamp1'}]});
  expect(api.calls.some(c=>c.path==='cache/delete')).toBe(false);
});

test('native node file picker rejects oversize files and can reread the same file', async ({page}) => {
  const api = await mockAPI(page);
  await page.goto('/#/settings');
  await page.getByRole('combobox', {name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox', {name:'代理后端',exact:true}).selectOption('mihomo');
  const picker = page.getByLabel('或选择节点文件'), draft = page.getByRole('textbox', {name:'粘贴节点内容',exact:true});
  await picker.setInputFiles({name:'too-large.yaml',mimeType:'text/yaml',buffer:Buffer.alloc(2*1024*1024+1)});
  await expect(page.getByText(/节点文件超过 2 MiB/)).toBeVisible();
  await expect(draft).toHaveValue('');
  const file = {name:'nodes.yaml',mimeType:'text/yaml',buffer:Buffer.from('proxies: []')};
  await picker.setInputFiles(file); await expect(draft).toHaveValue('proxies: []');
  await page.getByRole('button', {name:'清除本次导入草稿'}).click();
  await expect(draft).toHaveValue('');
  await picker.setInputFiles(file); await expect(draft).toHaveValue('proxies: []');
  expect(api.calls.some(c=>c.path==='proxy/import'||c.path==='settings')).toBe(false);
});

test('download list releases focus to its panel when polling removes the active row', async ({page}) => {
  await mockAPI(page);
  let present = true;
  await page.route('**/api/downloads', r=>r.fulfill({json:{bytesPerSecond:1024,tasks:present?[{id:'task',resource:'fixture-resource',songs:[{id:1,title:'下载中的歌曲'}],stage:'download_and_hash',bytes:1024,size:2048,bytesPerSecond:1024,idleMS:0}]:[]}}));
  await page.clock.install(); await page.goto('/#/monitor');
  const row = page.getByRole('grid', {name:'下载任务'}).getByRole('row');
  await row.focus(); await page.keyboard.press('ArrowRight');
  const details = row.getByRole('button', {name:'下载详情'});
  await expect(details).toBeFocused(); await page.keyboard.press('Enter');
  await expect(row.getByText('资源：fixture-resource')).toBeVisible();
  present = false; await page.clock.fastForward(5100);
  await expect(row).toHaveCount(0); await expect(page.locator('#downloads')).toBeFocused();
});

test('diagnostic gauge preserves thresholds and stays readable in narrow theme variants', async ({page}, info) => {
  const api = await mockAPI(page);
  await page.setViewportSize({width:320,height:800});
  await page.goto('/#/setup');
  await navigateStep(page,'3. 网络');
  for (const [ratio,label] of [[0,'不可用'],[1,'不可用'],[2,'不稳定'],[2.01,'可用'],[256,'可用'],[512,'可用']] as const) {
    Object.assign(api.diagnostics.calibration, {revision:++api.diagnostics.calibration.revision,run:1,phase:'completed',singleSpeedBPS:500000,fullSpeedBPS:ratio*2000000/8,fullConcurrency:8});
    await api.emit('calibration');
    const gauge=page.getByRole('group', {name:`满路 ${ratio.toFixed(2)} 倍，${label}`,exact:true});
    await expect(gauge).toBeVisible();
    await expect(gauge.locator('.fui-gc__root')).toBeVisible();
    await expect(gauge.getByRole('option',{name:'≤1 倍不可用',exact:true})).toBeVisible();
    await expect(gauge.getByRole('option',{name:'1 至 2 倍不稳定',exact:true})).toBeVisible();
    await expect(gauge.getByRole('option',{name:'>2 倍可用',exact:true})).toBeVisible();
    await expect(page.getByText(`${(ratio*2).toFixed(2)} Mbps`, {exact:true}).last()).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  }
  for (const theme of ['light','dark','forced'] as const) {
    await page.emulateMedia({colorScheme:theme==='dark'?'dark':'light',forcedColors:theme==='forced'?'active':'none',reducedMotion:'reduce'});
    await expect(page.getByRole('button', {name:'开始CF 并发测速'})).toBeEnabled();
    await page.screenshot({path:info.outputPath(`diagnostics-${theme}.png`),fullPage:true,animations:'disabled'});
  }
  expect(api.calls.some(c=>c.path.endsWith('/start'))).toBe(false);
});

test('standard gauge distinguishes live samples, missing samples and restored results', async ({page},info) => {
  const api=await mockAPI(page);
  await page.goto('/#/setup');await navigateStep(page,'3. 网络');
  await expect(page.getByText('尚无速度样本',{exact:true})).toBeVisible();
  await expect(page.locator('.fui-gc__root')).toHaveCount(0);
  Object.assign(api.diagnostics.calibration,{revision:2,run:1,running:true,phase:'running',measurementStage:'single',liveSpeedBPS:375000,
    lastValid:{direct:{singleSpeedBPS:500000,fullSpeedBPS:2000000,fullConcurrency:8,referenceBitrateBPS:2000000}}});
  await api.emit('calibration');
  const live=page.getByRole('group',{name:'当前 1.50 倍，不稳定（测量中）',exact:true});
  await expect(live).toBeVisible();
  await live.getByRole('option',{name:'≤1 倍不可用',exact:true}).focus();
  await page.keyboard.press('ArrowRight');
  await expect(live.getByRole('option',{name:'1 至 2 倍不稳定',exact:true})).toBeFocused();
  await page.getByRole('heading',{name:'网络',exact:true}).focus();
  await page.setViewportSize({width:320,height:800});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('gauge-live.png'),fullPage:true,animations:'disabled'});
  Object.assign(api.diagnostics.calibration,{revision:3,running:false,phase:'canceled'});await api.emit('calibration');
  await expect(page.getByRole('group',{name:'满路 8.00 倍，可用',exact:true})).toBeVisible();
  await expect(page.getByText('已恢复上次结果',{exact:true})).toBeVisible();
  expect(api.calls.some(c=>c.path.endsWith('/start'))).toBe(false);
});
