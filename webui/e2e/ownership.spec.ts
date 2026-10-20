import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

test('download controls and resulting progress share one page',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/downloads',r=>r.fulfill({json:{bytesPerSecond:12000,tasks:[{id:'1',resource:'song',songs:[{id:'1',title:'正在准备的歌曲'}],stage:'download',bytes:100,size:1000,bytesPerSecond:12000,idleMS:0}]}}));
  await page.goto('/#/library');
  await expect(page.getByRole('heading',{level:1,name:'下载管理',exact:true})).toBeVisible();
  await expect(page.getByText('正在准备的歌曲',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'开启队列预加载',exact:true}).click();
  await expect(page.getByRole('button',{name:'下载缺失视频',exact:true})).toBeVisible();
  expect(api.calls.some(c=>c.path==='queue/start')).toBe(true);
  await expect(page.getByRole('button',{name:'下载诊断',exact:true})).toHaveAttribute('aria-expanded','false');
});

test('video import, storage and coverage are found from local videos',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/cache');
  const coverage=page.getByRole('heading',{name:'曲库覆盖率',exact:true});
  await expect(coverage).toBeVisible();
  expect((await coverage.boundingBox())!.y).toBeLessThan((await page.getByRole('grid',{name:'本地视频文件'}).boundingBox())!.y);
  await page.getByRole('link',{name:'导入视频',exact:true}).click();
  await page.getByRole('textbox',{name:'外部目录',exact:true}).fill('D:\\Videos');
  await page.getByRole('button',{name:'扫描并导入',exact:true}).click();
  expect(api.calls.find(c=>c.path==='import/start')?.body.source).toBe('D:\\Videos');
  await page.getByRole('link',{name:'存储设置',exact:true}).click();
  await expect(page.getByRole('radio',{name:'不保留',exact:true})).toBeVisible();
});

test('separate settings locations save only their own draft and preserve the others',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/library');
  await page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true}).fill('777');
  await navigate(page,'本地视频');
  await page.getByRole('link',{name:'存储设置',exact:true}).click();
  await page.getByRole('radio',{name:'限制为',exact:true}).check();
  await page.getByRole('spinbutton',{name:/视频存储预算/}).fill('20');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'立即重启并应用',exact:true})).toBeEnabled();
  expect(api.state.settings.prepareAheadSeconds).not.toBe(777);
  await navigate(page,'下载管理');
  await expect(page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true})).toHaveValue('777');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(api.state.settings.maxCacheBytes).toBe(20*2**30);
  expect(api.state.settings.prepareAheadSeconds).toBe(777);
});
