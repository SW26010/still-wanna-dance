import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
import {coverOnFirstFrame} from './cover-frame.mjs';
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
  await expect.poll(()=>page.evaluate(()=>window.coverDecodes)).toBe(51);
  const source=await first.getAttribute('src'),initialReads=reads.length;
  const forward=await coverOnFirstFrame(page,'51',()=>page.getByRole('button',{name:'下一页曲库',exact:true}).click());
  expect(forward).toContainEqual(expect.objectContaining({complete:true,width:480,visibility:'visible'}));
  const backward=await coverOnFirstFrame(page,'1',()=>page.getByRole('button',{name:'上一页曲库',exact:true}).click());
  expect(backward).toContainEqual({source,complete:true,width:480,visibility:'visible'});
  expect(reads).toHaveLength(initialReads);expect(errors).toEqual([]);
  await writeFile(join(output,'result.json'),JSON.stringify({decodedBeforePaging:51,initialReads,readsAfterPaging:reads.length,forward,backward,errors},null,2));
}finally{await browser.close();}
