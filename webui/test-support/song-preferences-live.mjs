import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const [baseURL,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(baseURL+'/#/library?view=songs');
  const panel=page.locator('#library-browser');
  await expect(panel).toBeVisible();
  const preference=(id,policy='收藏')=>panel.getByRole('group',{name:`歌曲 ${id} 的偏好`,exact:true}).getByRole('button',{name:policy,exact:true});
  await expect(preference(3)).toHaveAttribute('aria-pressed','false');
  await preference(3).click();
  await expect(preference(3)).toHaveAttribute('aria-pressed','true');
  await expect(panel.getByRole('row').filter({hasText:'已保存的收藏测试'})).toContainText('已保存');
  await preference(1).click();
  await expect(page.getByText('歌曲 1 已设为收藏。',{exact:true})).toBeVisible();
  await expect(preference(1)).toHaveAttribute('aria-pressed','true');
  await expect(panel.getByRole('row').filter({hasText:'未下载的收藏测试'})).toContainText('未保存');
  await expect(panel.getByRole('row').filter({hasText:'已保存的收藏测试'})).toContainText('已保存');
  await preference(2,'屏蔽').click();
  await expect(page.getByText('歌曲 2 已设为屏蔽。',{exact:true})).toBeVisible();
  await expect(preference(2,'屏蔽')).toHaveAttribute('aria-pressed','true');
  expect(await (await page.request.post(baseURL+'/__preferences/reopen')).json()).toEqual({ok:true,wasRunning:false});
  expect(await (await page.request.get(baseURL+'/__preferences/probe')).json()).toEqual({status:403,resolutions:0});
  await panel.getByRole('combobox',{name:'筛选',exact:true}).selectOption('favorite');
  await panel.getByRole('button',{name:'查找',exact:true}).click();
  await expect(panel.getByRole('row')).toHaveCount(3);
  await preference(1).focus();
  await preference(1).click();
  await expect(panel.getByRole('row')).toHaveCount(2);
  await expect(panel).toBeFocused();
  await panel.getByRole('combobox',{name:'筛选',exact:true}).selectOption('');
  await panel.getByRole('button',{name:'查找',exact:true}).click();
  await preference(1).click();
  await expect(preference(1)).toHaveAttribute('aria-pressed','true');
  expect((await page.request.post(baseURL+'/__preferences/reopen')).ok()).toBe(true);
  await page.reload();
  await expect(preference(1)).toHaveAttribute('aria-pressed','true');
  await expect(preference(2,'屏蔽')).toHaveAttribute('aria-pressed','true');
  await expect(panel.getByRole('row').filter({hasText:'未下载的收藏测试'})).toContainText('未保存');
  for(const [width,theme] of [[1280,'light'],[390,'dark']]) {
    await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:theme});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:join(output,`preferences-${width}-${theme}.png`),fullPage:true,animations:'disabled'});
  }
  await page.goto(baseURL+'/#batch');
  const batch=page.locator('#batch');
  await expect(batch.getByRole('button',{name:'下载缺失视频',exact:true})).toBeEnabled();
  await expect(batch).toContainText('当前存储预算为“不保留”。');
  await expect(batch.getByRole('link',{name:'存储设置',exact:true})).toHaveAttribute('href','#videoBudgetGiB');
  await expect(batch).toContainText('已开启优先保留收藏视频，收藏视频仍可保留。');
  expect(errors).toEqual([]);
  await writeFile(join(output,'browser.json'),JSON.stringify({passed:true,errors,checks:['offline favorite preserves existing file without starting engine','preference changes','file state separate','filter and focus','engine reopen persistence','blocked GET 403','zero network resolutions','zero-budget manual action','desktop and narrow themes']},null,2));
  console.log('Real Console song preference, file state, reopen and blocked GET checks passed.');
} finally {await browser.close();}
