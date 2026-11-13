import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
const [baseURL,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}}),errors=[],selections=[],legacyStateReads=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{if(new URL(request.url()).pathname==='/api/library/covers')legacyStateReads.push(request.url());});
  page.on('response',async response=>{
    const url=new URL(response.url());
    if(url.pathname==='/api/library'&&url.searchParams.get('covers')==='1'&&response.ok())selections.push((await response.json()).covers);
  });
  const state=async()=>await (await page.request.get(baseURL+'/test/cover-state')).json();
  await page.goto(baseURL+'/#/library');await expect(page.getByRole('list',{name:'曲库封面'})).toBeVisible();
  const first=page.getByRole('group',{name:'歌曲 1',exact:true});
  await expect(first.locator('img.is-ready')).toHaveJSProperty('naturalWidth',480);
  const missing=page.getByRole('group',{name:'歌曲 2',exact:true});
  await expect(missing.locator('.library-cover-play')).toHaveCSS('background-image','none');
  await expect(missing.locator('img.is-ready')).toHaveAttribute('src',/category-covers\/fitness-marshall\.webp/);
  const artwork=await page.evaluate(async()=>{
    const files=['just-dance-solo','fitness-marshall','default'];
    return Promise.all(files.map(async file=>{
      const image=new Image();image.src='/assets/webui/category-covers/'+file+'.webp';await image.decode();
      return {file,width:image.naturalWidth,height:image.naturalHeight};
    }));
  });
  expect(artwork.every(image=>image.width===320&&image.height===240)).toBe(true);
  await expect(page.getByRole('group',{name:'歌曲 3',exact:true}).locator('img.is-ready')).toHaveJSProperty('naturalWidth',480);
  await expect.poll(async()=>(await state()).requests).toBe(3);
  const source=await first.locator('img.is-ready').getAttribute('src');
  const original=await first.locator('img.is-ready').elementHandle();
  expect(selections[0]['1'].source).toBe('category');
  await first.getByRole('button',{name:'收藏',exact:true}).click();
  await expect(first.getByRole('button',{name:'收藏',exact:true})).toHaveAttribute('aria-pressed','true');
  await expect.poll(()=>selections.at(-1)['1'].source).toBe('local');
  await expect.poll(async()=>(await state()).requests).toBe(4);
  await expect(first.locator('img.is-ready')).toHaveAttribute('src',source);
  expect(await original.evaluate(image=>image.isConnected)).toBe(true);
  await page.getByRole('button',{name:'切换到列表视图',exact:true}).click();
  await page.getByRole('button',{name:'切换到封面视图',exact:true}).click();
  await expect(first.locator('img.is-ready')).toHaveAttribute('src',source);
  await expect.poll(async()=>(await state()).requests).toBe(5);
  await page.mouse.move(0,0);await page.keyboard.press('Escape');
  await page.screenshot({path:join(output,'covers-light.png')});
  await page.request.get(baseURL+'/test/offline');
  let release;
  const pending=new Promise(resolve=>release=resolve);
  await page.route('**/api/library/cover?**',async route=>{
    const query=new URL(route.request().url()).searchParams;
    if(query.get('id')==='1'&&query.get('mode')==='cached')await pending;
    await route.continue();
  });
  await page.reload();await expect(page.getByRole('list',{name:'曲库封面'})).toBeVisible();
  try {
    await expect(first.locator('.library-cover-play')).toHaveCSS('background-image','none');
    await expect(first.locator('img')).toHaveCount(0);
    await page.screenshot({path:join(output,'cached-local-loading-neutral.png')});
  }finally{release();}
  await expect(first.locator('img.is-ready')).toHaveJSProperty('naturalWidth',480);
  await expect(first.getByRole('button',{name:'收藏',exact:true})).toHaveAttribute('aria-pressed','true');
  // The two saved images were downloaded in this program session. Only the
  // still-missing second image is retried after reloading the browser page.
  await expect.poll(async()=>(await state()).requests).toBe(6);
  await expect(first.locator('img.is-ready')).toHaveJSProperty('naturalWidth',480);
  await page.emulateMedia({colorScheme:'dark'});await page.setViewportSize({width:390,height:844});
  await first.scrollIntoViewIfNeeded();expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.mouse.move(0,0);await page.keyboard.press('Escape');
  await page.screenshot({path:join(output,'covers-dark-narrow.png')});
  expect((await state()).engine).toBe(false);expect(errors).toEqual([]);expect(legacyStateReads).toEqual([]);
  expect(selections.at(-1)['1'].source).toBe('local');expect(selections.at(-1)['2'].source).toBe('category');
  await writeFile(join(output,'result.json'),JSON.stringify({defaultView:'covers',firstPageRequests:3,requestsAfterSourcePromotion:4,requestsAfterViewReturn:5,requestsAfterBrowserReload:6,sourcePromotionRetainsJPEGElement:true,coverViewSurvivedReload:true,cachedJPEGURLReused:true,localLoadingStaysNeutral:true,selections,legacyStateReads,sessionCachedUpdatesSkipped:true,offlineCachedImagesVisible:true,missingCategoryArtworkVisible:true,artwork,favoritePersisted:true,engineStarted:false,errors},null,2));
}finally{await browser.close();}
