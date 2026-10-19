import {navigate} from './navigation';
import { test, expect } from '@playwright/test';
import { mockAPI } from './api';

test('drafts survive polling and navigation; save preserves active config and blocks dirty restart', async ({page}) => {
  const api=await mockAPI(page); await page.goto('/#/settings');
  const m=page.getByRole('spinbutton',{name:/提前准备时间 M/});
  await expect(m).toHaveAccessibleDescription(/当前值约占 125 MB 临时缓存，按 2 Mbps 估算/);
  await m.fill(''); await expect(m).not.toHaveAccessibleDescription(/当前值约占/);
  await m.fill('100'); await expect(m).toHaveAccessibleDescription(/当前值约占 25 MB 临时缓存/);
  await navigate(page,'开始使用');
  await page.getByRole('button',{name:'启动播放网关',exact:true}).click();
  await expect(page.getByText('操作已完成。',{exact:true})).toBeVisible();
  await navigate(page,'设置'); await expect(m).toHaveValue('100');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'立即重启并应用'})).toBeEnabled();
  expect(api.state.settings.prepareAheadSeconds).toBe(100); expect(api.state.activeSettings.prepareAheadSeconds).toBe(500);
  await m.fill('145'); await expect(page.getByRole('button',{name:'立即重启并应用'})).toBeDisabled();
  await expect(m).toHaveAccessibleDescription(/当前值约占 36\.25 MB 临时缓存/);
  await page.getByRole('button',{name:'放弃更改',exact:true}).click(); await expect(m).toHaveValue('100');
  await expect(m).toHaveAccessibleDescription(/当前值约占 25 MB 临时缓存/);
  await m.fill('16000'); await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'立即重启并应用'})).toBeEnabled();
  expect(api.state.settings.prepareAheadSeconds).toBe(16000); expect(api.state.activeSettings.prepareAheadSeconds).toBe(500);
  await expect(m).toHaveAccessibleDescription(/当前值约占 4000 MB 临时缓存/);
  for (const value of ['99','16001']) {
    await m.fill(value); await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await expect(m).not.toHaveAccessibleDescription(/当前值约占/);
    expect(api.calls.filter(c=>c.path==='settings')).toHaveLength(2);
  }
  expect(api.calls.every(c=>c.token==='browser-fixture-token')).toBe(true);
});
test('cache renders Unix millisecond request times and zero as no record', async ({page}) => {
  const api=await mockAPI(page), errors:string[]=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto('/#/cache');
  const rows=page.getByRole('grid',{name:'缓存文件'}).getByRole('row');
  const requested=await page.evaluate(at=>new Date(at).toLocaleString(),api.files[0].lastRequest);
  await expect(rows.first()).toContainText('最近请求：'+requested);
  await expect(rows.nth(1)).toContainText('最近请求：无记录');
  await page.getByRole('button',{name:'刷新文件列表'}).click();
  await expect(rows.first()).toContainText('最近请求：'+requested);
  await navigate(page,'设置'); await navigate(page,'本地缓存');
  await expect(rows.first().getByRole('checkbox')).toBeEnabled();
  expect(errors).toEqual([]);
});

test('cache protects in-use files and only deletes confirmed stamped selections', async ({page}) => {
  const api=await mockAPI(page); await page.goto('/#/cache');
  await expect(page.getByRole('checkbox',{name:/测试歌曲 2/})).toBeDisabled();
  await page.getByRole('button',{name:'选择本页可删除项'}).click();
  await page.getByRole('button',{name:'删除选中缓存'}).click(); await expect(page.getByRole('dialog')).toBeVisible();
  await page.getByRole('button',{name:'取消',exact:true}).click(); expect(api.calls.some(c=>c.path==='cache/delete')).toBe(false);
  await page.getByRole('button',{name:'删除选中缓存'}).click(); await page.getByRole('button',{name:'确认删除',exact:true}).click();
  await expect(page.getByRole('checkbox',{name:/测试歌曲 1/})).toHaveCount(0);
  expect(api.calls.find(c=>c.path==='cache/delete')?.body).toEqual({storageID:'fixture',entries:[{key:'key1',stamp:'stamp1'}]});
});
test('pause only freezes monitor display; native dialogs replaced by Fluent confirmation', async ({page}) => {
  const api=await mockAPI(page); await page.goto('/#/monitor');
  await page.getByRole('checkbox',{name:'暂停下载与请求刷新'}).check();
  const reads=api.calls.filter(c=>c.path==='downloads'||c.path==='requests').length;
  await page.getByRole('button',{name:'立即检测',exact:true}).click();
  await expect(page.getByText('操作已完成。',{exact:true})).toBeVisible();
  expect(api.calls.filter(c=>c.path==='downloads'||c.path==='requests')).toHaveLength(reads);
  await page.getByRole('button',{name:'退出整个应用'}).click(); await expect(page.getByRole('dialog')).toBeVisible();
  await page.keyboard.press('Escape'); await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(api.calls.some(c=>c.path==='exit')).toBe(false);
});
test('all console pages render at narrow width without runtime errors', async ({page}, info) => {
  await mockAPI(page); await page.setViewportSize({width:390,height:844}); await page.emulateMedia({colorScheme:'dark'});
  const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto('/');
  for(const name of ['开始使用','准备歌曲','本地缓存','运行状态','设置']) {
    await navigate(page,name);
    await expect(page.locator('#main')).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:info.outputPath(name+'.png'),fullPage:true,animations:'disabled'});
  }
  expect(errors).toEqual([]);
});
