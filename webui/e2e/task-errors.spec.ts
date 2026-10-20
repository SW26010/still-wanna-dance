import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigateStep} from './navigation';

test('sample preparation explains online requirements and belongs to the selected connection',async({page},info)=>{
  const api=await mockAPI(page);
  Object.assign(api.diagnostics.calibration,{run:1,phase:'failed',reason:'preparation_insufficient'});
  await api.emit('calibration');
  await page.setViewportSize({width:390,height:844});
  await page.goto('/#/setup');await navigateStep(page,'3. 网络');
  const error=page.getByText(/未能取得足够的在线测速样本/);
  await expect(error).toBeVisible();
  await expect(page.getByText(/无需导入本地视频或修改存储预算/)).toBeVisible();
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
    phase:cached?'本地检查完成；可通过下载补齐获取缺失视频':'暂无可用的歌曲清单，未开始本地检查。请重试在线更新。',
    catalog:{revision:cached?'fixture-v1':''},total:cached?2:0,checked:cached?2:0,hits:1,missing:1,failed:0};
  if(cached)api.state.lastBatch={...api.state.batch,updated:'2026-10-21T00:00:00Z'};
  await page.route('**/api/batch/scan',async r=>{
    api.calls.push({path:'batch/scan',body:r.request().postDataJSON()});
    api.state.batch={scanOnly:true,running:true,phase:'正在更新歌曲清单'};
    await r.fulfill({json:{ok:true}});
  });
  await page.setViewportSize({width:390,height:844});await page.goto('/#/library');
  const panel=page.locator('#batch');
  await expect(panel.getByText(cached?'在线曲库更新失败，本次使用已有清单。':'在线曲库更新失败，本地没有可用清单，暂时无法检查视频。',{exact:true})).toBeVisible();
  if(cached){
    await expect(panel.getByText(/上次完成的本地检查（使用已有清单）/)).toBeVisible();
    await expect(panel.getByText(/本地检查失败 0/)).toBeVisible();
    await expect(panel.getByText(/下载完成 0 · 失败 0/)).toHaveCount(0);
  }else await expect(panel.getByText('暂无完成的任务记录',{exact:true})).toBeVisible();
  const raw=panel.getByText('Get "https://catalog.example/": EOF',{exact:true});
  await expect(raw).toBeHidden();
  await panel.screenshot({path:info.outputPath('catalog-error.png')});
  await panel.getByRole('button',{name:'在线更新错误详情',exact:true}).click();
  await expect(raw).toBeVisible();
  await panel.getByRole('button',{name:'重试更新',exact:true}).click();
  await expect(panel.getByText('正在更新歌曲清单',{exact:true})).toBeVisible();
  await expect(panel.getByRole('button',{name:'重试更新',exact:true})).toHaveCount(0);
  expect(api.calls.filter(c=>c.path==='batch/scan')).toHaveLength(1);
  expect(api.calls.some(c=>c.path==='batch/switch')).toBe(false);
});
