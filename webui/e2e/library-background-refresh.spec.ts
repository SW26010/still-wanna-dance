import {test,expect,type Page} from '@playwright/test';
import {mockAPI} from './api';
import {expectWithClock} from './clock';

async function heldRefresh(page:Page) {
  const api=await mockAPI(page);
  let hold=false,reads=0,release!:()=>void;
  const pending=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/library?*',async route=>{
    reads++;
    if(hold)await pending;
    await route.fallback();
  });
  await page.clock.install();
  return {api,release,reads:()=>reads,start:async()=>{
    hold=true;
    const previous=reads;
    api.state.catalogSync={initializing:false,checking:false,updated:'2026-11-12T00:00:00Z'};
    await expectWithClock(page,()=>reads).toBeGreaterThan(previous);
  }};
}

for(const [view,theme] of [['songs','light'],['songs','dark'],['resources','light']] as const)test(`${view} background refresh preserves controls and focus in ${theme}`,async({page},info)=>{
  const refresh=await heldRefresh(page);
  await page.emulateMedia({colorScheme:theme});
  await page.goto('/#/library?view='+view);
  const table=page.getByRole('table',{name:view==='songs'?'曲库歌曲':'曲库资源'});
  const row=table.locator('tbody tr').first(),preview=row.getByRole('button',{name:'预览',exact:true});
  await expect(preview).toBeEnabled();
  const original=await preview.elementHandle();
  const appearance=()=>row.getByRole('button').evaluateAll(buttons=>buttons.map(button=>{
    const style=getComputedStyle(button),icon=button.querySelector('svg');
    return {name:button.getAttribute('aria-label'),color:style.color,background:style.backgroundColor,opacity:style.opacity,icon:icon&&getComputedStyle(icon).color};
  }));
  const before=await appearance(),bounds=await table.boundingBox();
  await preview.focus();
  await page.screenshot({path:info.outputPath('before.png'),animations:'disabled'});
  try {
    await refresh.start();
    await expect(table).toHaveAttribute('aria-busy','true');
    await expect(preview).toBeEnabled();
    await expect(preview).toBeFocused();
    await expect(row.getByRole('button',{name:'定位文件',exact:true})).toBeEnabled();
    await expect(row.getByRole('button',{name:'删除本地视频',exact:true})).toBeEnabled();
    if(view==='songs')await expect(row.getByRole('button',{name:'收藏',exact:true})).toBeEnabled();
    await expect(page.getByRole('button',{name:'查找',exact:true})).toBeEnabled();
    expect(await appearance()).toEqual(before);
    expect(await table.boundingBox()).toEqual(bounds);
    expect(await original!.evaluate(el=>el.isConnected)).toBe(true);
    await page.screenshot({path:info.outputPath('refreshing.png'),animations:'disabled'});
    // An action uses the displayed snapshot even while a newer read is pending.
    await row.getByRole('button',{name:'定位文件',exact:true}).click();
    await expect.poll(()=>refresh.api.calls.find(c=>c.path==='cache/open')?.body).toEqual({storageID:'fixture',entries:[{key:'key1',stamp:'stamp1'}]});
  } finally {refresh.release();}
  await expect(table).toHaveAttribute('aria-busy','false');
  expect(await original!.evaluate(el=>el.isConnected)).toBe(true);
});

test('query changes keep retained rows locked until the matching result arrives',async({page})=>{
  const refresh=await heldRefresh(page);
  await page.goto('/#/library');
  const preview=page.getByRole('table',{name:'曲库歌曲'}).getByRole('button',{name:'预览',exact:true}).first();
  await expect(preview).toBeEnabled();
  try {
    await refresh.start();
    await page.getByRole('searchbox',{name:'搜索曲库',exact:true}).fill('新的查询');
    await page.getByRole('button',{name:'查找',exact:true}).click();
    await expect(preview).toBeDisabled();
    await expect(page.getByRole('button',{name:'定位文件',exact:true}).first()).toBeDisabled();
  } finally {refresh.release();}
  await expect(preview).toBeEnabled();
});

test('an unchanged deletion can be confirmed while background refresh is pending',async({page})=>{
  const refresh=await heldRefresh(page);
  await page.goto('/#/library');
  await page.getByRole('button',{name:'删除本地视频',exact:true}).first().click();
  const dialog=page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  try {
    await refresh.start();
    await dialog.getByRole('button',{name:'确认删除',exact:true}).click();
    await expect.poll(()=>refresh.api.calls.find(c=>c.path==='cache/delete')?.body).toEqual({storageID:'fixture',entries:[{key:'key1',stamp:'stamp1'}]});
  } finally {refresh.release();}
  await expect(page.getByRole('table',{name:'曲库歌曲'})).toHaveAttribute('aria-busy','false');
  await expect(page.getByRole('group',{name:'本地视频',exact:true})).toContainText('1 个');
});

test('saving during background refresh waits for the post-save revision before allowing another change',async({page})=>{
  const refresh=await heldRefresh(page);
  await page.goto('/#/library');
  const favorite=page.getByRole('button',{name:'收藏',exact:true}).first();
  await expect(favorite).toBeEnabled();
  try {
    await refresh.start();
    await favorite.click();
    await expect.poll(()=>refresh.api.calls.filter(c=>c.path==='songs').length).toBe(1);
    await expect(page.getByRole('listitem').filter({hasText:'歌曲 1 已设为收藏。'})).toBeVisible();
    await expect(favorite).toBeDisabled();
    // Keyboard activation must not submit a second mutation using pre-save data.
    await favorite.press('Enter');
    expect(refresh.api.calls.filter(c=>c.path==='songs')).toHaveLength(1);
  } finally {refresh.release();}
  await expect(favorite).toBeEnabled();
});

for(const [width,theme] of [[1280,'light'],[390,'dark']] as const)test(`cover refresh preserves images and open details at ${width}px`,async({page},info)=>{
  const refresh=await heldRefresh(page);
  let coverReads=0;
  await page.route('**/api/library/covers?*',route=>route.fulfill({json:{cached:[],refresh:[]}}));
  await page.route('**/api/library/cover?*',route=>{
    coverReads++;
    return route.fulfill({contentType:'image/svg+xml',body:'<svg xmlns="http://www.w3.org/2000/svg" width="480" height="360"><rect width="480" height="360" fill="#426c68"/></svg>'});
  });
  await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:theme});
  await page.goto('/#/library');
  await page.getByRole('button',{name:'切换到封面视图',exact:true}).click();
  const grid=page.getByRole('list',{name:'曲库封面'}),card=grid.getByRole('group',{name:'歌曲 1',exact:true});
  const image=card.locator('img'),favorite=card.getByRole('button',{name:'收藏',exact:true});
  await expect(image).toBeVisible();await expect.poll(()=>coverReads).toBe(2);
  const source=await image.getAttribute('src'),original=await image.elementHandle();
  await card.getByRole('button',{name:'歌曲 1 的操作',exact:true}).click();
  await page.getByRole('menuitem',{name:'详情',exact:true}).click();
  const details=page.getByRole('group',{name:'歌曲 1 的详情',exact:true});
  await expect(details).toBeVisible();
  const focused=await page.evaluateHandle(()=>document.activeElement);
  try {
    await refresh.start();
    await expect(grid).toHaveAttribute('aria-busy','true');
    await expect(favorite).toBeEnabled();
    await expect(card.getByRole('button',{name:/^预览歌曲/})).toBeEnabled();
    await expect(details).toBeVisible();
    expect(await focused.evaluate(el=>el===document.activeElement)).toBe(true);
    await page.screenshot({path:info.outputPath('cover-refreshing.png'),animations:'disabled'});
  } finally {refresh.release();}
  await expect(grid).toHaveAttribute('aria-busy','false');
  await expect(details).toBeVisible();
  expect(await image.getAttribute('src')).toBe(source);
  expect(await original!.evaluate(el=>el.isConnected)).toBe(true);
  expect(coverReads).toBe(2);
});
