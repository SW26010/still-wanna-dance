import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigateStep} from './navigation';

test('manual library download leaves the running queue alone',async({page})=>{
  const api=await mockAPI(page);
  api.state.queue={desired:true,running:true,songs:[{songId:42}]};
  await page.goto('/#/library');
  await page.locator('#batch').getByRole('button',{name:'下载缺失视频',exact:true}).click();
  expect(api.calls.filter(c=>/^(batch\/|queue\/)/.test(c.path)).map(c=>c.path)).toEqual(['batch/start']);
  await expect(page.locator('#queue')).not.toContainText('暂停');
});

test('sample preparation explains online requirements and belongs to the selected connection',async({page},info)=>{
  const api=await mockAPI(page);
  Object.assign(api.diagnostics.calibration,{run:1,phase:'failed',reason:'preparation_insufficient'});
  await api.emit('calibration');
  await page.setViewportSize({width:390,height:844});
  await page.goto('/#/setup');await navigateStep(page,'3. 网络');
  const error=page.getByText(/在线测速样本不足/);
  await expect(error).toBeVisible();
  await expect(error).toContainText('检查连通性后重试');
  await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeEnabled();
  await expect(page.getByRole('button',{name:'测速技术详情',exact:true})).toHaveAttribute('aria-expanded','false');
  await expect(page.getByRole('navigation',{name:'引导步骤',exact:true})).toBeHidden();
  await page.screenshot({path:info.outputPath('sample-error.png'),fullPage:true,animations:'disabled'});
  const upstream=page.getByRole('combobox',{name:'检测上游',exact:true});
  await upstream.selectOption('proxy');await expect(error).toBeHidden();
  await upstream.selectOption('direct');await expect(error).toBeVisible();
  await page.getByRole('button',{name:'开始下载速度测试',exact:true}).click();
  await expect(error).toBeHidden();
  expect(api.calls.filter(c=>c.path==='setup/network/calibration/start')).toHaveLength(1);
});

for(const cached of [true,false])test(`local scan distinguishes online catalog failure and retries (${cached})`,async({page},info)=>{
  const api=await mockAPI(page);
  api.state.batch={scanOnly:true,running:false,catalogWarning:'Get "https://catalog.example/": EOF',
    phase:cached?'本地检查完成；可通过下载缺失视频获取缺失视频':'暂无可用的歌曲清单，未开始本地检查。请重试在线更新。',
    catalog:{revision:cached?'fixture-v1':''},total:cached?2:0,checked:cached?2:0,hits:1,missing:1,failed:0};
  if(cached)api.state.lastBatch={...api.state.batch,updated:'2026-10-21T00:00:00Z'};
  await page.route('**/api/batch/scan',async r=>{
    api.calls.push({path:'batch/scan',body:r.request().postDataJSON()});
    api.state.batch={scanOnly:true,running:true,phase:'正在更新歌曲清单'};
    await r.fulfill({json:{ok:true}});
  });
  await page.setViewportSize({width:390,height:844});await page.goto('/#/library');
  const panel=page.locator('#batch');
  await expect(panel.getByText(cached?'在线曲库更新失败，本次使用已有清单。':'没有可用清单，请重试更新。',{exact:true})).toBeVisible();
  if(cached){await panel.getByRole('button',{name:'上次完成的批量任务',exact:true}).click();await expect(page.locator('.fui-PopoverSurface:visible')).toContainText('共 2 · 已有 1 · 缺失 1');await page.keyboard.press('Escape');}
  else await expect(panel.getByRole('button',{name:'上次完成的批量任务',exact:true})).toHaveCount(0);
  await panel.screenshot({path:info.outputPath('catalog-error.png')});
  await panel.getByRole('button',{name:'在线更新错误详情',exact:true}).click();
  await expect(page.locator('.fui-PopoverSurface:visible')).toContainText('Get "https://catalog.example/": EOF');await page.keyboard.press('Escape');
  await panel.getByRole('button',{name:'重试更新',exact:true}).click();
  await expect(panel.getByText('正在更新歌曲清单',{exact:true})).toBeVisible();
  await expect(panel.getByRole('button',{name:'重试更新',exact:true})).toHaveCount(0);
  expect(api.calls.filter(c=>c.path==='batch/scan')).toHaveLength(1);
  expect(api.calls.some(c=>c.path==='batch/start')).toBe(false);
});
