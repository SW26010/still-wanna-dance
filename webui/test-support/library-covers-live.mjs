import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
const [baseURL,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  const state=async()=>await (await page.request.get(baseURL+'/test/cover-state')).json();
  await page.goto(baseURL+'/#/library');await expect(page.getByRole('list',{name:'曲库封面'})).toBeVisible();
  const first=page.getByRole('group',{name:'歌曲 1',exact:true});
  await expect(first.locator('img')).toHaveJSProperty('naturalWidth',480);
  await expect(page.getByRole('group',{name:'歌曲 2',exact:true})).toContainText('暂无封面');
  await expect.poll(async()=>(await state()).requests).toBe(3);
  await first.getByRole('button',{name:'收藏',exact:true}).click();
  await expect(first.getByRole('button',{name:'收藏',exact:true})).toHaveAttribute('aria-pressed','true');
  expect((await state()).requests).toBe(3);
  await page.mouse.move(0,0);await page.keyboard.press('Escape');
  await page.screenshot({path:join(output,'covers-light.png')});
  await page.request.get(baseURL+'/test/offline');
  await page.reload();await expect(page.getByRole('list',{name:'曲库封面'})).toBeVisible();
  await expect(first.locator('img')).toHaveJSProperty('naturalWidth',480);
  await expect(first.getByRole('button',{name:'收藏',exact:true})).toHaveAttribute('aria-pressed','true');
  // The two saved images were downloaded in this program session. Only the
  // still-missing second image is retried after reloading the browser page.
  await expect.poll(async()=>(await state()).requests).toBe(4);
  await expect(first.locator('img')).toHaveJSProperty('naturalWidth',480);
  await page.emulateMedia({colorScheme:'dark'});await page.setViewportSize({width:390,height:844});
  await first.scrollIntoViewIfNeeded();expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.mouse.move(0,0);await page.keyboard.press('Escape');
  await page.screenshot({path:join(output,'covers-dark-narrow.png')});
  expect((await state()).engine).toBe(false);expect(errors).toEqual([]);
  await writeFile(join(output,'result.json'),JSON.stringify({defaultView:'covers',firstPageRequests:3,requestsAfterBrowserReload:4,coverViewSurvivedReload:true,sessionCachedUpdatesSkipped:true,offlineCachedImagesVisible:true,favoritePersisted:true,engineStarted:false,errors},null,2));
}finally{await browser.close();}
