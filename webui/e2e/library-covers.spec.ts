import {test,expect,type Page} from '@playwright/test';
import {readFileSync} from 'node:fs';
import {Buffer} from 'node:buffer';
import {mockAPI} from './api';
import {coverOnFirstFrame,libraryOnClickFrame} from '../test-support/cover-frame.mjs';
import type {LibraryCoverChoice,LibraryPage,LibrarySong} from '../src/types';

async function trackCoverURLs(page:Page) {
  await page.addInitScript(()=>{
    const state={active:new Map<string,number>(),created:0,revoked:0};
    const create=URL.createObjectURL.bind(URL),revoke=URL.revokeObjectURL.bind(URL);
    URL.createObjectURL=blob=>{const source=create(blob);state.active.set(source,blob.size);state.created++;return source;};
    URL.revokeObjectURL=source=>{state.active.delete(source);state.revoked++;revoke(source);};
    Object.assign(window,{coverURLState:state});
  });
  return ()=>page.evaluate(()=>{
    const state=(window as Window&{coverURLState:{active:Map<string,number>;created:number;revoked:number}}).coverURLState;
    return {active:[...state.active.keys()],bytes:[...state.active.values()].reduce((total,size)=>total+size,0),created:state.created,revoked:state.revoked};
  });
}

async function jpegFixture(page:Page) {
  return Buffer.from(await page.evaluate(()=>{
    const canvas=document.createElement('canvas');canvas.width=480;canvas.height=360;
    const context=canvas.getContext('2d')!;context.fillStyle='#9bc8b3';context.fillRect(0,0,480,360);
    return canvas.toDataURL('image/jpeg').split(',')[1];
  }),'base64');
}

async function coversFixture(page:Page,count=3,image?:Buffer) {
  const api=await mockAPI(page),requested:string[]=[],modes:string[]=[],queries:URLSearchParams[]=[],checked=new Set<string>();
  const key='a'.repeat(32),local={...api.files[0],key},songs:LibrarySong[]=Array.from({length:count},(_,i)=>({id:String(i+1),title:`封面测试歌曲 ${i+1}`,artist:'测试歌手',dancer:'测试舞者',group:'测试社团',policy:'normal',score:0.12345,hasDemand:i!==1,resource:key}));
  const choices:Record<string,LibraryCoverChoice>=Object.fromEntries(songs.map(song=>[song.id,{source:song.id==='1'||song.id==='3'?'local':'category'}]));
  await page.route('**/api/library?**',async route=>{
    const q=new URL(route.request().url()).searchParams;queries.push(q);
    const text=q.get('q')||'',offset=Number(q.get('offset')||0),selected=songs.filter(s=>text.startsWith('id:')?s.id===text.slice(3):s.title.includes(text));
    const data:LibraryPage={storageID:'fixture',databaseID:'fixture-db',songs:selected.slice(offset,offset+50),resources:{[key]:{key,size:1024,duration:null,songs:[{id:'1',title:songs[0].title}],songCount:1,local}},keys:[key],total:selected.length,fileCount:1,totalBytes:1024};
    if(q.get('covers')==='1')data.covers=Object.fromEntries(data.songs.map(song=>[song.id,{...choices[song.id],refresh:choices[song.id].source==='local'&&(choices[song.id].refresh??!checked.has(song.id))}]));
    await route.fulfill({json:data});
  });
  await page.route('**/api/songs',async route=>{
    const body=route.request().postDataJSON(),song=songs.find(s=>s.id===body.id)!;
    song.policy=body.policy;api.calls.push({path:'songs',body});await route.fulfill({json:{ok:true}});
  });
  await page.route('**/api/library/cover?**',async route=>{
    const id=new URL(route.request().url()).searchParams.get('id')!;requested.push(id);modes.push(new URL(route.request().url()).searchParams.get('mode')||'fetch');
    expect(route.request().headers()['x-swd-token']).toBe('browser-fixture-token');
    if(new URL(route.request().url()).searchParams.get('mode')!=='cached')checked.add(id);
    if(id==='2')return route.fulfill({status:404,body:'暂无封面'});
    if(new URL(route.request().url()).searchParams.get('mode')!=='cached')choices[id]={source:'local',refresh:false};
    await route.fulfill({contentType:image?'image/jpeg':'image/svg+xml',body:image??'<svg xmlns="http://www.w3.org/2000/svg" width="480" height="360"><rect width="480" height="360" fill="#9bc8b3"/><path d="M0 290 150 120 260 230 380 80 480 180V360H0" fill="#426c68"/></svg>'});
  });
  return {...api,songs,requested,modes,queries,choices};
}

test('all bundled category images decode at the recorded size',async({page})=>{
  await coversFixture(page);await page.goto('/#/library');
  const manifest=JSON.parse(readFileSync(new URL('../../docs/research/category-cover-assets-20261112.json',import.meta.url),'utf8'));
  const paths:string[]=manifest.assets.map((asset:{file:string})=>'/'+asset.file.replace('internal/console/',''));
  const pixels=await page.evaluate(async paths=>Promise.all(paths.map(async path=>{
    const image=new Image();image.src=path;await image.decode();return [image.naturalWidth,image.naturalHeight];
  })),paths);
  expect(pixels).toHaveLength(manifest.assetCount);
  expect(pixels.every(pixel=>pixel[0]===manifest.encoding.pixels[0]&&pixel[1]===manifest.encoding.pixels[1])).toBe(true);
});

test('page choices keep pending local images neutral and select category artwork only for missing images',async({page},info)=>{
  const {songs}=await coversFixture(page,4);
  songs[0].group='Just Dance Solo';songs[1].group='Fitness Marshall';songs[2].group='K-POP Dance Practice';songs[3].group='__proto__';
  let release!:()=>void;
  const pending=new Promise<void>(resolve=>release=resolve);
  await page.route('**/api/library/cover?**',async route=>{
    const id=new URL(route.request().url()).searchParams.get('id');
    if(id==='1'){
      await pending;
      return route.fulfill({contentType:'image/svg+xml',body:'<svg xmlns="http://www.w3.org/2000/svg" width="480" height="360"><rect width="480" height="360" fill="#9bc8b3"/></svg>'});
    }
    if(id==='3')return route.fulfill({contentType:'image/jpeg',body:'not an image'});
    return route.fulfill({status:404,body:'暂无封面'});
  });
  await page.setViewportSize({width:1280,height:900});await page.goto('/#/library');
  const buttons=songs.map(song=>page.getByRole('group',{name:`歌曲 ${song.id}`,exact:true}).locator('.library-cover-play'));
  try {
    await expect(buttons[0]).toHaveCSS('background-image','none');
    await expect(buttons[0].locator('img')).toHaveCount(0);
    for(const [index,slug] of [[1,'fitness-marshall'],[2,'default'],[3,'default']] as const){
      const image=buttons[index].locator('img.is-ready');
      await expect(image).toHaveAttribute('src',new RegExp(`/category-covers/${slug}\\.webp`));
      await expect(image).toHaveJSProperty('naturalWidth',320);
      const box=(await buttons[index].boundingBox())!;expect(box.width/box.height).toBeCloseTo(4/3,2);
    }
    await page.mouse.move(0,0);await page.screenshot({path:info.outputPath('category-fallbacks.png'),animations:'disabled'});
    await page.emulateMedia({colorScheme:'dark'});await page.setViewportSize({width:390,height:844});
    await page.screenshot({path:info.outputPath('category-fallbacks-dark-narrow.png'),animations:'disabled'});
  }finally{release();}
  await expect(buttons[0].locator('img.is-ready')).toHaveJSProperty('naturalWidth',480);
  await page.setViewportSize({width:1280,height:900});await page.emulateMedia({colorScheme:'light'});
  await page.screenshot({path:info.outputPath('real-cover-replaces-category.png'),animations:'disabled'});
});

test('category images use a neutral surface until their selected asset loads',async({page},info)=>{
  const {choices}=await coversFixture(page);
  for(const id of Object.keys(choices))choices[id]={source:'category'};
  await page.route('**/api/library/cover?**',route=>route.fulfill({status:404,body:'missing'}));
  let release!:()=>void;
  const pending=new Promise<void>(resolve=>release=resolve);
  await page.route('**/assets/webui/category-covers/default.webp',async route=>{await pending;await route.continue();});
  await page.goto('/#/library');
  const button=page.getByRole('group',{name:'歌曲 1',exact:true}).locator('.library-cover-play');
  try {
    await expect(button).toHaveCSS('background-image','none');
    await expect(button.locator('img')).toBeHidden();
    await expect(button).toHaveCSS('background-color','rgb(245, 245, 245)');
    await page.screenshot({path:info.outputPath('neutral-category-loading-light.png'),animations:'disabled'});
    await page.emulateMedia({colorScheme:'dark'});await page.setViewportSize({width:390,height:844});
    await expect(button).toHaveCSS('background-color','rgb(20, 20, 20)');
    await page.screenshot({path:info.outputPath('neutral-category-loading-dark.png'),animations:'disabled'});
  }finally{release();}
  await expect(button.locator('img.is-ready')).toHaveJSProperty('naturalWidth',320);
});

test('default cover view loads current and next pages in priority order and preserves toolbar',async({page})=>{
  const {requested,modes,queries,songs}=await coversFixture(page,75);
  songs.splice(0,50,...songs.slice(0,50).reverse());
  await page.setViewportSize({width:1280,height:800});
  await page.goto('/#/library');
  const search=page.getByRole('searchbox',{name:'搜索曲库'}),tabs=page.getByRole('tablist',{name:'曲库视图'});
  const before={search:await search.boundingBox(),tabs:await tabs.boundingBox()};
  const grid=page.getByRole('list',{name:'曲库封面'});
  await expect(grid.getByRole('listitem')).toHaveCount(50);
  await expect.poll(()=>requested.includes('1')).toBe(true);
  await expect.poll(()=>requested.includes('75')).toBe(true);
  await expect.poll(()=>modes.filter(mode=>mode==='refresh').length).toBe(2);
  expect(requested.slice(0,6)).toEqual(['3','1','50','49','48','47']);
  const network=requested.map((id,i)=>({id,mode:modes[i]})).filter(r=>r.mode!=='cached');
  expect(network.slice(0,48).every(r=>Number(r.id)<=50&&r.mode==='fetch')).toBe(true);
  expect(network.slice(48,73).every(r=>Number(r.id)>50&&r.mode==='fetch')).toBe(true);
  expect(network.slice(73).every(r=>r.mode==='refresh')).toBe(true);
  await expect(grid.locator('img').first()).toHaveJSProperty('naturalWidth',480);
  expect(await search.boundingBox()).toEqual(before.search);expect(await tabs.boundingBox()).toEqual(before.tabs);
  expect(queries.at(-1)?.get('sort')).toBe('score');
  expect(requested.includes('50')).toBe(true);expect(requested.includes('51')).toBe(true);
  await page.getByRole('button',{name:'切换到列表视图',exact:true}).click();
  await expect(page.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  expect(await search.boundingBox()).toEqual(before.search);expect(await tabs.boundingBox()).toEqual(before.tabs);
  await page.getByRole('tab',{name:'按资源',exact:true}).click();
  await expect(page.getByRole('button',{name:'切换到封面视图',exact:true})).toBeHidden();
  await expect(page.getByRole('table',{name:'曲库资源'})).toBeVisible();
});

for(const failure of ['page','choices'] as const)test(`next-page ${failure} failure leaves current cached covers and update checks available`,async({page})=>{
  const {modes}=await coversFixture(page,75);let failed=false;
  if(failure==='page')await page.route('**/api/library?**',async route=>{
    if(new URL(route.request().url()).searchParams.get('offset')==='50'){failed=true;await route.fulfill({status:503,body:'下一页暂时不可用'});}
    else await route.fallback();
  });
  else await page.route('**/api/library?**',async route=>{
    if(new URL(route.request().url()).searchParams.get('offset')==='50'){failed=true;await route.fulfill({json:{songs:[]}});}
    else await route.fallback();
  });
  await page.goto('/#/library');
  await expect.poll(()=>failed).toBe(true);
  for(const id of ['1','3'])await expect(page.getByRole('group',{name:`歌曲 ${id}`,exact:true}).locator('img')).toHaveJSProperty('naturalWidth',480);
  await expect.poll(()=>modes.filter(mode=>mode==='refresh').length).toBe(2);
});

test('returning to cover view reuses images without rereading files or another session update',async({page})=>{
  const {modes}=await coversFixture(page);await page.goto('/#/library');
  await expect.poll(()=>modes.filter(mode=>mode==='refresh').length).toBe(2);
  await page.getByRole('button',{name:'切换到列表视图',exact:true}).click();
  await page.getByRole('button',{name:'切换到封面视图',exact:true}).click();
  await expect.poll(()=>modes.filter(mode=>mode==='fetch').length).toBe(2);
  expect(modes.filter(mode=>mode==='cached')).toHaveLength(2);
  expect(modes.filter(mode=>mode==='refresh')).toHaveLength(2);
  await expect(page.getByRole('list',{name:'曲库封面'}).locator('img.is-ready')).toHaveCount(3);
});

test('paging shows cached JPEGs on the first frame without rereading their files',async({page})=>{
  const jpeg=await jpegFixture(page);
  const {modes,choices,calls}=await coversFixture(page,51,jpeg);
  for(const id of Object.keys(choices))choices[id]={source:id==='2'?'category':'local',refresh:id==='3'};
  let paused=false,rereads=0;
  await page.route('**/api/library/cover?**',async route=>{
    if(paused&&new URL(route.request().url()).searchParams.get('id')!=='2'){rereads++;await route.abort();}
    else await route.fallback();
  });
  await page.goto('/#/library');
  await expect.poll(()=>modes.filter(mode=>mode==='refresh').length).toBe(1);
  const first=page.getByRole('group',{name:'歌曲 1',exact:true}).locator('.library-cover-play img.is-ready');
  await expect(first).toHaveJSProperty('naturalWidth',480);
  const original=await first.getAttribute('src');
  paused=true;
  const forward=await coverOnFirstFrame(page,'51',()=>page.getByRole('button',{name:'下一页曲库',exact:true}).click());
  expect(forward).toContainEqual(expect.objectContaining({complete:true,width:480,visibility:'visible'}));
  const next=page.getByRole('group',{name:'歌曲 51',exact:true}).locator('.library-cover-play');
  await expect(next).toHaveCSS('background-image','none');
  await expect(next.locator('img.is-ready')).toHaveJSProperty('naturalWidth',480);
  await expect(next.locator('img')).toHaveAttribute('src',/^blob:/);
  const backward=await coverOnFirstFrame(page,'1',()=>page.getByRole('button',{name:'上一页曲库',exact:true}).click());
  expect(backward).toContainEqual({source:original,complete:true,width:480,visibility:'visible'});
  await expect(first).toHaveAttribute('src',original!);
  await expect(first).toBeVisible();
  expect(rereads).toBe(0);expect(calls.some(call=>call.path==='library/covers')).toBe(false);
});

test('paging keeps old covers at their current position until the destination page is committed',async({page})=>{
  const jpeg=await jpegFixture(page),{choices,modes}=await coversFixture(page,100,jpeg);
  for(const id of Object.keys(choices))choices[id]={source:id==='2'?'category':'local',refresh:id==='3'};
  await page.goto('/#/library');
  await expect.poll(()=>modes.filter(mode=>mode==='refresh').length).toBe(1);
  for(const [buttonName,offset,from,to] of [['下一页曲库','50','1','51'],['上一页曲库','0','51','1']]){
    const button=page.getByRole('button',{name:buttonName,exact:true});
    await button.scrollIntoViewIfNeeded();
    const position=await page.evaluate(()=>scrollY);expect(position).toBeGreaterThan(500);
    let release!:()=>void;
    const pending=new Promise<void>(resolve=>release=resolve);
    await page.route('**/api/library?**',async route=>{
      if(new URL(route.request().url()).searchParams.get('offset')===offset)await pending;
      await route.fallback();
    });
    try {
      const frame=await libraryOnClickFrame(page,()=>button.click());
      expect(frame.firstSong).toBe('歌曲 '+from);
      expect(frame.scrollY).toBe(position);
    }finally{release();}
    await expect(page.getByRole('group',{name:'歌曲 '+to,exact:true}).locator('img.is-ready')).toHaveJSProperty('naturalWidth',480);
    await expect.poll(()=>page.locator('#library-browser').evaluate(section=>section.getBoundingClientRect().top)).toBe(0);
    await expect(page.locator('#library-browser')).toBeFocused();
  }
});

test('undecodable refreshes retain loaded images and release rejected URLs',async({page})=>{
  const sources=await trackCoverURLs(page);
  await coversFixture(page);
  await page.route('**/api/library/cover?**',async route=>{
    if(new URL(route.request().url()).searchParams.get('mode')==='refresh')return route.fulfill({contentType:'image/jpeg',body:'not an image'});
    await route.fallback();
  });
  await page.goto('/#/library');
  await expect.poll(sources).toMatchObject({created:4,revoked:2});
  const active=(await sources()).active;expect(active).toHaveLength(2);
  for(const id of ['1','3']){
    const image=page.getByRole('group',{name:`歌曲 ${id}`,exact:true}).locator('img.is-ready');
    await expect(image).toHaveJSProperty('naturalWidth',480);
    expect(active).toContain(await image.getAttribute('src'));
  }
});

test('recent cover reuse retains at most three pages and releases evicted URLs',async({page})=>{
  const sources=await trackCoverURLs(page);
  const {modes,choices}=await coversFixture(page,200);
  for(const id of Object.keys(choices))choices[id]={source:id==='2'?'category':'local',refresh:['1','51','101'].includes(id)};
  await page.goto('/#/library');
  await expect.poll(sources).toMatchObject({created:100,revoked:1});
  const original=await page.getByRole('group',{name:'歌曲 1',exact:true}).locator('img').getAttribute('src');
  for(const checks of [2,3]){
    await page.getByRole('button',{name:'下一页曲库',exact:true}).click();
    await expect.poll(()=>modes.filter(mode=>mode==='refresh').length).toBe(checks);
  }
  await expect.poll(sources).toMatchObject({created:202,revoked:52});
  const active=(await sources()).active;
  expect(active).toHaveLength(150);expect(active).not.toContain(original);
  const current=page.getByRole('group',{name:'歌曲 101',exact:true}).locator('img');
  await expect(current).toHaveJSProperty('naturalWidth',480);
  expect(active).toContain(await current.getAttribute('src'));
});

test('retained images respect their byte budget beyond the current working page',async({page})=>{
  const sources=await trackCoverURLs(page),jpeg=await jpegFixture(page),size=700*1024;
  await coversFixture(page,75,Buffer.concat([jpeg,Buffer.alloc(size-jpeg.length)]));
  const retained=Math.floor(8*1024*1024/size),current=49;
  await page.goto('/#/library');
  await expect.poll(sources).toMatchObject({created:76,revoked:76-current-retained});
  const state=await sources();
  expect(state.active).toHaveLength(current+retained);
  expect(state.bytes-current*size).toBeLessThanOrEqual(8*1024*1024);
  for(const id of ['1','50'])await expect(page.getByRole('group',{name:`歌曲 ${id}`,exact:true}).locator('img.is-ready')).toHaveJSProperty('naturalWidth',480);
});

test('cancelled image decoding cannot retain obsolete cover URLs',async({page})=>{
  const sources=await trackCoverURLs(page),{modes}=await coversFixture(page);
  await page.addInitScript(()=>{
    const decode=HTMLImageElement.prototype.decode;
    let blocked=true,release!:()=>void;
    const pending=new Promise<void>(resolve=>release=resolve);
    HTMLImageElement.prototype.decode=async function(){await decode.call(this);if(blocked)await pending;};
    Object.assign(window,{releaseCoverDecoding:()=>{blocked=false;release();}});
  });
  await page.goto('/#/library');
  await expect.poll(sources).toMatchObject({created:2,revoked:0});
  const aborted=(await sources()).active;
  await page.getByRole('button',{name:'切换到列表视图',exact:true}).click();
  await expect(page.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  await page.evaluate(()=>(window as Window&{releaseCoverDecoding:()=>void}).releaseCoverDecoding());
  await expect.poll(sources).toMatchObject({active:[],created:2,revoked:2});
  await page.getByRole('button',{name:'切换到封面视图',exact:true}).click();
  const image=page.getByRole('group',{name:'歌曲 1',exact:true}).locator('img');
  await expect(image).toHaveJSProperty('naturalWidth',480);
  expect(aborted).not.toContain(await image.getAttribute('src'));
  expect(modes.filter(mode=>mode==='cached')).toHaveLength(4);
});

test('selected list view survives navigation, reload and reopening until the next program run',async({page,context})=>{
  const first=await coversFixture(page);await page.goto('/#/library');
  await expect(page.getByRole('list',{name:'曲库封面'})).toBeVisible();
  await page.getByRole('button',{name:'切换到列表视图',exact:true}).click();
  await page.getByRole('tab',{name:'按资源',exact:true}).click();await page.getByRole('tab',{name:'按歌曲',exact:true}).click();
  await expect(page.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  await page.goto('/#/library?song=1');await expect(page.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  await page.getByRole('searchbox',{name:'搜索曲库'}).fill('');await page.getByRole('button',{name:'查找',exact:true}).click();
  await page.reload();await expect(page.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  const other=await context.newPage(),second=await coversFixture(other);await other.goto('/#/library');
  await expect(other.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  await other.getByRole('button',{name:'切换到封面视图',exact:true}).click();
  await expect(page.getByRole('list',{name:'曲库封面'})).toBeVisible();
  await other.getByRole('button',{name:'切换到列表视图',exact:true}).click();
  await expect(page.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  await page.close();await other.close();
  const reopened=await context.newPage(),third=await coversFixture(reopened);await reopened.goto('/#/library');
  await expect(reopened.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  await reopened.evaluate(()=>localStorage.setItem('swd-library-list-run','previous-run'));
  await reopened.reload();await expect(reopened.getByRole('list',{name:'曲库封面'})).toBeVisible();
  expect([...first.calls,...second.calls,...third.calls].filter(call=>call.path==='settings')).toEqual([]);
});

test('cards share preference, preview, details and file actions with the list',async({page})=>{
  const {calls}=await coversFixture(page);
  const preparations:unknown[]=[];
  await page.route('**/api/library/preview',async route=>{preparations.push(route.request().postDataJSON());await route.fulfill({json:{url:'/media/preview?cover-test'}});});
  await page.route('**/media/preview?**',route=>route.fulfill({status:502,body:'fixture'}));
  await page.goto('/#/library');
  const card=page.getByRole('group',{name:'歌曲 1',exact:true}),image=card.getByRole('button',{name:'预览歌曲 1：封面测试歌曲 1',exact:true});
  await card.hover();
  const details=page.getByRole('group',{name:'歌曲 1 的详情',exact:true});
  await expect(details).toContainText('测试歌手');await expect(details).toContainText('测试舞者 / 测试社团');await expect(details).toContainText('12.35');await expect(details).toContainText('已保存');
  await page.keyboard.press('Escape');
  await image.click();await expect(page.getByLabel('曲库预览播放器')).toHaveAttribute('src','/media/preview?cover-test');
  expect(preparations).toEqual([{storageID:'fixture',songID:'1'}]);
  await page.keyboard.press('Escape');await expect(image).toBeFocused();
  await card.getByRole('button',{name:'收藏',exact:true}).click();
  await expect(card.getByRole('button',{name:'收藏',exact:true})).toHaveAttribute('aria-pressed','true');
  await page.mouse.move(0,0);
  await expect(card.locator('.library-cover-policy')).toHaveCSS('background-color','rgba(0, 0, 0, 0)');
  await expect(card.getByRole('button',{name:'收藏',exact:true})).toHaveCSS('background-color','rgba(0, 0, 0, 0)');
  expect(preparations).toHaveLength(1);
  await card.getByRole('button',{name:'屏蔽',exact:true}).click();
  await expect(card.getByRole('button',{name:'收藏',exact:true})).toHaveAttribute('aria-pressed','false');
  await expect(card.getByRole('button',{name:'屏蔽',exact:true})).toHaveAttribute('aria-pressed','true');
  await expect(image).toBeDisabled();
  await card.getByRole('button',{name:'歌曲 1 的操作',exact:true}).click();
  await expect(page.getByRole('menuitem',{name:'预览',exact:true})).toHaveAttribute('aria-disabled','true');
  await expect(page.getByRole('menuitem',{name:'下载歌曲',exact:true})).toBeDisabled();
  await page.getByRole('menuitem',{name:'定位文件',exact:true}).click();
  await expect.poll(()=>calls.some(c=>c.path==='cache/open')).toBe(true);
  await card.getByRole('button',{name:'歌曲 1 的操作',exact:true}).click();await page.getByRole('menuitem',{name:'删除本地视频',exact:true}).click();
  await expect(page.getByRole('dialog')).toContainText('删除');await page.getByRole('button',{name:'取消',exact:true}).click();
  await expect(card.getByRole('button',{name:'歌曲 1 的操作',exact:true})).toBeFocused();
  await page.getByRole('button',{name:'切换到列表视图',exact:true}).click();
  await expect(page.getByRole('group',{name:'歌曲 1 的偏好',exact:true}).getByRole('button',{name:'屏蔽',exact:true})).toHaveAttribute('aria-pressed','true');
});

test('four downloads run at once and changing pages cancels the previous demand',async({page})=>{
  await coversFixture(page,120);
  const requested:string[]=[],release:(()=>void)[]=[];
  await page.route('**/api/library/cover?**',async route=>{
    const q=new URL(route.request().url()).searchParams;
    if(q.get('mode')==='cached')return route.fulfill({status:404,body:'missing'});
    requested.push(q.get('id')!);
    await new Promise<void>(resolve=>release.push(resolve));
    await route.fulfill({status:404,body:'missing'}).catch(()=>{});
  });
  await page.goto('/#/library');
  await expect.poll(()=>requested.length).toBe(4);
  expect(requested.every(id=>Number(id)<=50)).toBe(true);
  await page.getByRole('button',{name:'下一页曲库',exact:true}).click();
  await expect(page.getByRole('list',{name:'曲库封面'}).getByRole('group',{name:'歌曲 51',exact:true})).toBeVisible();
  await expect.poll(()=>requested.length).toBe(8);
  expect(requested.slice(4).every(id=>Number(id)>=51&&Number(id)<=100)).toBe(true);
  await page.getByRole('button',{name:'切换到列表视图',exact:true}).click();
  for(const done of release)done();
  await page.getByRole('button',{name:'查找',exact:true}).click();await expect(page.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  expect(requested).toHaveLength(8);
});

test('card details opened from the menu dismiss on an outside click',async({page})=>{
  await coversFixture(page);await page.goto('/#/library');
  const card=page.getByRole('group',{name:'歌曲 1',exact:true});
  await card.getByRole('button',{name:'歌曲 1 的操作',exact:true}).click();
  await page.getByRole('menuitem',{name:'详情',exact:true}).click();
  const detail=page.getByRole('group',{name:'歌曲 1 的详情',exact:true});await expect(detail).toBeVisible();
  await page.getByRole('heading',{name:'曲库',exact:true}).click();
  await expect(detail).toBeHidden();
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('hovering the cover shows details without a preview tooltip',async({page})=>{
  await coversFixture(page);await page.clock.install();await page.goto('/#/library');
  await page.getByRole('button',{name:'预览歌曲 1：封面测试歌曲 1',exact:true}).hover();
  await page.clock.runFor(1000);
  await expect(page.getByRole('group',{name:'歌曲 1 的详情',exact:true})).toBeVisible();
  await expect(page.getByRole('tooltip')).toHaveCount(0);
});

test('hover details wait for intent, stay readable and ignore card actions',async({page})=>{
  await coversFixture(page);await page.clock.install({time:new Date('2026-11-12T00:00:00Z')});await page.clock.pauseAt(new Date('2026-11-12T00:00:01Z'));await page.goto('/#/library');
  const card=page.getByRole('group',{name:'歌曲 1',exact:true}),image=card.getByRole('button',{name:'预览歌曲 1：封面测试歌曲 1',exact:true});
  const detail=page.getByRole('group',{name:'歌曲 1 的详情',exact:true});
  await image.hover();await page.clock.runFor(499);await expect(detail).toBeHidden();
  await page.clock.runFor(1);await expect(detail).toBeVisible();
  await expect(page.getByRole('tooltip')).toHaveCount(0);
  await detail.hover();await page.clock.runFor(1000);await expect(detail).toBeVisible();
  await page.keyboard.press('Escape');await expect(detail).toBeHidden();
  for(const name of ['收藏','屏蔽','歌曲 1 的操作']){
    await card.getByRole('button',{name,exact:true}).hover();await page.clock.runFor(600);
    await expect(detail).toBeHidden();
  }
  await image.hover();await page.clock.runFor(200);await page.mouse.move(0,0);
  await page.clock.runFor(1000);await expect(detail).toBeHidden();
});

for(const change of ['search','page'] as const)test(`pending hover details are canceled when changing ${change}`,async({page})=>{
  const {modes}=await coversFixture(page,75);
  await page.setViewportSize(change==='page'?{width:3840,height:2160}:{width:1280,height:900});
  await page.clock.install({time:new Date('2026-11-12T00:00:00Z')});await page.clock.pauseAt(new Date('2026-11-12T00:00:01Z'));
  let changing=false,started=false,release!:()=>void;
  const response=new Promise<void>(resolve=>release=resolve);
  await page.route('**/api/library?**',async route=>{
    const q=new URL(route.request().url()).searchParams;
    if(changing&&(change==='page'?q.get('offset')==='50':q.get('q')==='封面')){started=true;await response;}
    await route.fallback();
  });
  await page.goto('/#/library');
  await expect.poll(()=>modes.filter(mode=>mode==='refresh').length).toBe(2);
  if(change==='search')await page.getByRole('searchbox',{name:'搜索曲库'}).focus();
  await page.getByRole('group',{name:'歌曲 1',exact:true}).getByRole('button',{name:'预览歌曲 1：封面测试歌曲 1',exact:true}).hover();
  await page.clock.runFor(200);changing=true;
  try {
    if(change==='page')await page.getByRole('button',{name:'下一页曲库',exact:true}).focus();
    else await page.keyboard.type('封面');
    await page.keyboard.press('Enter');await expect.poll(()=>started).toBe(true);
    await expect(page.getByRole('list',{name:'曲库封面'})).toHaveAttribute('aria-busy','true');
    await expect(page.getByRole('button',{name:'歌曲 1 的操作',exact:true})).toBeDisabled();
    await page.clock.runFor(300);
    await expect(page.locator('.library-cover-details:visible')).toHaveCount(0);
  }finally{release();}
  await expect(page.getByRole('list',{name:'曲库封面'})).toHaveAttribute('aria-busy','false');
  await expect(page.locator('.library-cover-details:visible')).toHaveCount(0);
  const id=change==='page'?'51':'1';
  await page.mouse.move(0,0);await page.getByRole('group',{name:`歌曲 ${id}`,exact:true}).hover({position:{x:40,y:40}});
  await page.clock.runFor(500);await expect(page.getByRole('group',{name:`歌曲 ${id} 的详情`,exact:true})).toBeVisible();
});

test('opening another song detail replaces the previous detail',async({page})=>{
  await coversFixture(page);await page.clock.install();await page.setViewportSize({width:1280,height:900});await page.goto('/#/library');
  await page.getByRole('button',{name:'歌曲 1 的操作',exact:true}).click();
  await page.getByRole('menuitem',{name:'详情',exact:true}).click();
  const first=page.getByRole('group',{name:'歌曲 1 的详情',exact:true}),second=page.getByRole('group',{name:'歌曲 2 的详情',exact:true});
  await expect(first).toBeVisible();
  await page.getByRole('group',{name:'歌曲 2',exact:true}).hover({position:{x:80,y:40}});await page.clock.runFor(500);
  await expect(second).toBeVisible();await expect(first).toBeHidden();
  await expect(page.locator('.library-cover-details:visible')).toHaveCount(1);
  await page.keyboard.press('Escape');await expect(second).toBeHidden();
});

test('the play indicator appears on hover or keyboard focus without changing actions',async({page},info)=>{
  await coversFixture(page);await page.goto('/#/library');
  const card=page.getByRole('group',{name:'歌曲 1',exact:true}),image=card.getByRole('button',{name:'预览歌曲 1：封面测试歌曲 1',exact:true});
  const indicator=image.locator('.library-cover-play-indicator');
  await page.mouse.move(0,0);await expect(indicator).toBeHidden();
  await image.hover();await expect(indicator).toBeVisible();
  await page.mouse.move(0,0);await expect(indicator).toBeHidden();
  await image.focus();await page.keyboard.press('Shift+Tab');await page.keyboard.press('Tab');
  await expect(image).toBeFocused();await expect(indicator).toBeVisible();
  await page.screenshot({path:info.outputPath('play-indicator.png'),animations:'disabled'});
  await page.keyboard.press('Tab');
  await card.getByRole('button',{name:'收藏',exact:true}).hover();await expect(indicator).toBeHidden();
});

for(const [width,theme,forcedColors] of [[1280,'light','none'],[390,'dark','none'],[320,'light','active']] as const)test(`cover layout and missing image at ${width}px ${theme} ${forcedColors}`,async({page},info)=>{
  const {requested,songs,choices}=await coversFixture(page);
  // This case checks layout across an unchanged query, with stable cover choices.
  for(const choice of Object.values(choices))choice.refresh=false;
  songs[0].title='很长的歌曲名称以及不能换行的英文歌曲名称LongSongTitle'.repeat(4);
  await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:theme,forcedColors});
  await page.goto('/#/library');
  const card=page.getByRole('group',{name:'歌曲 2',exact:true});
  await card.scrollIntoViewIfNeeded();await expect(card.locator('img.is-ready')).toHaveAttribute('src',/category-covers\/default\.webp/);
  await expect.poll(()=>requested.filter(id=>id==='2').length).toBe(1);
  await page.getByRole('button',{name:'查找',exact:true}).click();
  await expect(card.locator('img.is-ready')).toHaveAttribute('src',/category-covers\/default\.webp/);expect(requested.filter(id=>id==='2')).toHaveLength(1);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  const firstCard=page.getByRole('group',{name:'歌曲 1',exact:true}),title=firstCard.locator('.library-cover-title');
  await expect(title).toHaveText(songs[0].title);await expect(title).toHaveCSS('text-overflow','ellipsis');
  expect(await title.evaluate(el=>el.scrollWidth>el.clientWidth)).toBe(true);
  const idBox=(await firstCard.locator('.library-cover-id').boundingBox())!,titleBox=(await title.boundingBox())!,actionBox=(await firstCard.getByRole('button',{name:'歌曲 1 的操作',exact:true}).boundingBox())!;
  expect(titleBox.x).toBeGreaterThan(idBox.x+idBox.width);expect(titleBox.x+titleBox.width).toBeLessThanOrEqual(actionBox.x);
  await card.getByRole('button',{name:'歌曲 2 的操作',exact:true}).focus();await page.keyboard.press('Enter');
  await page.getByRole('menuitem',{name:'详情',exact:true}).click();
  const detail=page.getByRole('group',{name:'歌曲 2 的详情',exact:true});await expect(detail).toBeVisible();
  const box=(await detail.boundingBox())!;expect(box.x).toBeGreaterThanOrEqual(0);expect(box.x+box.width).toBeLessThanOrEqual(width);
  await page.screenshot({path:info.outputPath('cover-details.png'),animations:'disabled'});
  await page.keyboard.press('Escape');await page.mouse.move(0,0);
  await expect(card.getByRole('button',{name:'歌曲 2 的操作',exact:true})).toBeFocused();
  await page.getByRole('list',{name:'曲库封面'}).scrollIntoViewIfNeeded();
  await page.screenshot({path:info.outputPath('covers.png'),animations:'disabled'});
  await firstCard.getByRole('button',{name:'屏蔽',exact:true}).hover();
  await page.screenshot({path:info.outputPath('cover-policy-hover.png'),animations:'disabled'});
});
