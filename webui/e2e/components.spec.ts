import { test, expect } from '@playwright/test';
import { mockAPI } from './api';

test('resource file actions are reachable by standard keyboard navigation',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/library?view=resources');
  const rows=page.getByRole('table',{name:'曲库资源'}).locator('tbody tr');await expect(rows).toHaveCount(2);
  const remove=rows.first().getByRole('button',{name:'删除本地视频',exact:true});
  await remove.focus();await page.keyboard.press('Space');await expect(page.getByRole('dialog')).toBeVisible();
  await page.getByRole('dialog').getByRole('button',{name:'取消',exact:true}).focus();await page.keyboard.press('Enter');await expect(remove).toBeFocused();
  await expect(rows.nth(1).getByRole('button',{name:'删除本地视频',exact:true})).toBeDisabled();
  const open=rows.first().getByRole('button',{name:'定位文件'});await open.focus();await page.keyboard.press('Enter');
  await expect.poll(()=>api.calls.find(c=>c.path==='cache/open')?.body).toEqual({storageID:'fixture',entries:[{key:'key1',stamp:'stamp1'}]});
  const details=rows.first().getByRole('button',{name:'资源 key1 的详情',exact:true});
  await details.focus();await page.keyboard.press('Enter');
  const popup=page.getByRole('group',{name:'资源 key1 的详情',exact:true});
  await expect(popup.getByRole('link',{name:'测试歌曲 1（ID 1）',exact:true})).toBeFocused();
  await page.keyboard.press('Escape');await expect(popup).toBeHidden();
  await expect(details).toBeFocused();expect(api.calls.some(c=>c.path==='cache/delete')).toBe(false);
});

test('native node file picker rejects oversize files and can reread the same file', async ({page}) => {
  const api = await mockAPI(page);
  await page.goto('/#network-proxy');
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

test('diagnostic gauge preserves thresholds and stays readable in narrow theme variants', async ({page}, info) => {
  const api = await mockAPI(page);
  await page.setViewportSize({width:320,height:800});
  await page.goto('/#/network');
  for (const [ratio,label] of [[0,'不可用'],[1,'不可用'],[2,'不稳定'],[2.01,'可用'],[256,'可用'],[512,'可用']] as const) {
    Object.assign(api.diagnostics.calibration, {revision:++api.diagnostics.calibration.revision,run:1,phase:'completed',singleSpeedBPS:500000,fullSpeedBPS:ratio*2000000/8,fullConcurrency:8});
    await api.emit('calibration');
    const gauge=page.getByRole('group', {name:`多连接 ${ratio.toFixed(2)} 倍，${label}`,exact:true});
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
    await expect(page.getByRole('button', {name:'开始下载速度测试'})).toBeEnabled();
    await page.screenshot({path:info.outputPath(`diagnostics-${theme}.png`),fullPage:true,animations:'disabled'});
  }
  expect(api.calls.some(c=>c.path.endsWith('/start'))).toBe(false);
});

test('standard gauge distinguishes live samples, missing samples and restored results', async ({page},info) => {
  const api=await mockAPI(page);
  await page.goto('/#/network');
  await expect(page.getByText('尚未测速',{exact:true})).toBeVisible();
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
  await expect(page.getByRole('group',{name:'多连接 8.00 倍，可用',exact:true})).toBeVisible();
  await expect(page.getByText('已恢复上次结果',{exact:true})).toBeVisible();
  expect(api.calls.some(c=>c.path.endsWith('/start'))).toBe(false);
});
