import {navigateSettings,navigate} from './navigation';
import { test, expect } from '@playwright/test';
import { mockAPI } from './api';

test('drafts survive polling and navigation; save preserves active config and blocks dirty restart', async ({page}) => {
  const api=await mockAPI(page); await page.goto('/#prepareAheadSeconds');
  const m=page.getByRole('spinbutton',{name:/提前准备时间/});
  await expect(m).toHaveAccessibleDescription(/约 125 MB 临时缓存（按 2 Mbps）/);
  await m.fill(''); await expect(m).not.toHaveAccessibleDescription(/约 .* MB/);
  await m.fill('100'); await expect(m).toHaveAccessibleDescription(/约 25 MB 临时缓存/);
  await navigate(page,'网络');await page.getByRole('link',{name:'接入诊断',exact:true}).click();
  await page.getByRole('button',{name:'修复并开启加速',exact:true}).click();
  await expect(page.getByRole('listitem',{name:'视频加速已开启。',exact:true})).toBeVisible();
  await navigateSettings(page,'预加载与下载'); await expect(m).toHaveValue('100');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用'})).toBeEnabled();
  expect(api.state.settings.prepareAheadSeconds).toBe(100); expect(api.state.activeSettings.prepareAheadSeconds).toBe(500);
  await m.fill('145'); await expect(page.getByRole('button',{name:'重启并应用'})).toBeDisabled();
  await expect(m).toHaveAccessibleDescription(/约 36\.25 MB 临时缓存/);
  await page.getByRole('button',{name:'放弃更改',exact:true}).click(); await expect(m).toHaveValue('100');
  await expect(m).toHaveAccessibleDescription(/约 25 MB 临时缓存/);
  await m.fill('16000'); await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用'})).toBeEnabled();
  expect(api.state.settings.prepareAheadSeconds).toBe(16000); expect(api.state.activeSettings.prepareAheadSeconds).toBe(500);
  await expect(m).toHaveAccessibleDescription(/约 4000 MB 临时缓存/);
  for (const value of ['99','16001']) {
    await m.fill(value); await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await expect(m).not.toHaveAccessibleDescription(/约 .* MB/);
    expect(api.calls.filter(c=>c.path==='settings')).toHaveLength(2);
  }
  expect(api.calls.filter(c=>!c.path.includes('/events')).every(c=>c.token==='browser-fixture-token')).toBe(true);
});
test('cache renders Unix millisecond request times and zero as no record', async ({page}) => {
  const api=await mockAPI(page), errors:string[]=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto('/#/cache');
  const rows=page.getByRole('table',{name:'曲库资源'}).locator('tbody tr');
  const requested=await page.evaluate(at=>new Date(at).toLocaleString(),api.files[0].lastRequest);
  await rows.first().getByRole('button',{name:'资源 key1 的详情',exact:true}).click();
  await expect(page.locator('.fui-PopoverSurface:visible')).toContainText('最近请求：'+requested);await page.keyboard.press('Escape');
  await rows.nth(1).getByRole('button',{name:'资源 key2 的详情',exact:true}).click();
  await expect(page.locator('.fui-PopoverSurface:visible')).toContainText('最近请求：无记录');await page.keyboard.press('Escape');
  await page.getByRole('button',{name:'刷新曲库与本地占用'}).click();
  await navigateSettings(page,'预加载与下载'); await navigate(page,'曲库');
  await expect(rows.first().getByRole('checkbox')).toBeEnabled();
  expect(errors).toEqual([]);
});

test('cache protects in-use files and only deletes confirmed stamped selections', async ({page}) => {
  const api=await mockAPI(page); await page.goto('/#/cache');
  await expect(page.getByRole('checkbox',{name:/测试歌曲 2/})).toBeDisabled();
  await page.getByRole('button',{name:'选择本页可删除项'}).click();
  await page.getByRole('button',{name:'删除选中视频'}).click(); await expect(page.getByRole('dialog')).toBeVisible();
  await page.getByRole('button',{name:'取消',exact:true}).click(); expect(api.calls.some(c=>c.path==='cache/delete')).toBe(false);
  await page.getByRole('button',{name:'删除选中视频'}).click(); await page.getByRole('button',{name:'确认删除',exact:true}).click();
  await expect(page.getByRole('checkbox',{name:/测试歌曲 1/})).toHaveCount(0);
  expect(api.calls.find(c=>c.path==='cache/delete')?.body).toEqual({storageID:'fixture',entries:[{key:'key1',stamp:'stamp1'}]});
});
test('pause only freezes monitor display; exit tooltip supports keyboard focus without submitting', async ({page}) => {
  const api=await mockAPI(page); await page.goto('/#recent');
  await page.getByRole('checkbox',{name:'暂停请求刷新'}).check();
  const reads=api.calls.filter(c=>c.path==='downloads'||c.path==='requests').length;
  await navigate(page,'网络');await page.getByRole('button',{name:'检查当前连接',exact:true}).click();
  await expect(page.getByText('网络检测已开始，请查看检测结果。',{exact:true})).toBeVisible();
  expect(api.calls.filter(c=>c.path==='downloads'||c.path==='requests')).toHaveLength(reads);
  const exit=page.getByRole('button',{name:'退出程序',exact:true});
  await exit.focus();await expect(page.getByRole('tooltip')).toContainText('所有服务和后台任务将停止。');
  await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toHaveCount(0);await expect(exit).toBeFocused();
  expect(api.calls.some(c=>c.path==='exit')).toBe(false);
  await page.keyboard.press('Enter');await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect.poll(()=>api.calls.filter(c=>c.path==='exit').length).toBe(1);
});
test('all console pages render at narrow width without runtime errors', async ({page}, info) => {
  await mockAPI(page); await page.setViewportSize({width:390,height:844}); await page.emulateMedia({colorScheme:'dark'});
  const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto('/');
  for(const name of ['加速','历史','曲库','网络','设置','关于']) {
    await navigate(page,name);
    await expect(page.locator('#main')).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:info.outputPath(name+'.png'),fullPage:true,animations:'disabled'});
  }
  expect(errors).toEqual([]);
});
