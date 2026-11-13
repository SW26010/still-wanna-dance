import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
import {coverOnFirstFrame,libraryOnClickFrame} from './cover-frame.mjs';
const [baseURL,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}}),errors=[],reads=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{if(new URL(request.url()).pathname==='/api/library/cover')reads.push(request.url());});
  await page.addInitScript(()=>{
    const decode=HTMLImageElement.prototype.decode;
    window.coverDecodes=0;
    HTMLImageElement.prototype.decode=async function(){await decode.call(this);window.coverDecodes++;};
  });
  await page.goto(baseURL+'/#/library?sort=id&direction=ascending');
  const first=page.getByRole('group',{name:'歌曲 1',exact:true}).locator('img.is-ready');
  await expect(first).toHaveJSProperty('naturalWidth',480);
  await expect.poll(()=>page.evaluate(()=>window.coverDecodes)).toBe(100);
  const source=await first.getAttribute('src'),initialReads=reads.length;
  const frames=[];
  for(const [buttonName,offset,from,to] of [['下一页曲库','50','1','51'],['上一页曲库','0','51','1']]){
    const button=page.getByRole('button',{name:buttonName,exact:true});
    await button.scrollIntoViewIfNeeded();
    const position=await page.evaluate(()=>scrollY);expect(position).toBeGreaterThan(500);
    let release;
    const pending=new Promise(resolve=>release=resolve);
    await page.route('**/api/library?**',async route=>{
      if(new URL(route.request().url()).searchParams.get('offset')===offset)await pending;
      await route.continue();
    });
    let clicked;
    const destination=await coverOnFirstFrame(page,to,async()=>{
      try {
        clicked=await libraryOnClickFrame(page,()=>button.click());
        expect(clicked.firstSong).toBe('歌曲 '+from);expect(clicked.scrollY).toBe(position);
        await page.screenshot({path:join(output,offset+'-pending.png')});
      }finally{release();}
    });
    expect(destination).toContainEqual(expect.objectContaining({complete:true,width:480,visibility:'visible'}));
    if(to==='1')expect(destination).toContainEqual({source,complete:true,width:480,visibility:'visible'});
    await expect.poll(()=>page.locator('#library-browser').evaluate(section=>section.getBoundingClientRect().top)).toBe(0);
    await expect(page.locator('#library-browser')).toBeFocused();
    frames.push({offset,position,clicked,destination});
  }
  expect(reads).toHaveLength(initialReads);expect(errors).toEqual([]);
  await page.screenshot({path:join(output,'returned-page.png')});
  await writeFile(join(output,'result.json'),JSON.stringify({decodedBeforePaging:100,initialReads,readsAfterPaging:reads.length,frames,errors},null,2));
}finally{await browser.close();}
