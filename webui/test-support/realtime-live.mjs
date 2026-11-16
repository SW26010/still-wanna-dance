import {launchBrowser} from './browser.mjs';
import {expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output,view='network']=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await launchBrowser();
const result={browser:browser.version(),realConsole:true,view,errors:[],probes:[]};
const probe=page=>page.evaluate(async()=>{
  const start=performance.now();
  try {const response=await fetch('/__realtime/probe',{signal:AbortSignal.timeout(1800)});return {ok:response.ok,count:await response.json(),ms:performance.now()-start};}
  catch(error){return {ok:false,error:String(error),ms:performance.now()-start};}
});
try {
  const context=await browser.newContext(),pages=[];
  for(let i=0;i<6;i++){
    const page=await context.newPage();pages.push(page);
    page.on('pageerror',error=>result.errors.push(error.message));
    await page.goto(base+'/#/'+({network:'network',history:'timeline',blocks:'home'}[view]));
    if(view==='network'){
      await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeEnabled();
      await expect(page.getByRole('button',{name:'开始下载速度测试',exact:true})).toBeEnabled();
    }else if(view==='history')await expect(page.getByRole('heading',{name:'历史',exact:true})).toBeVisible();
    else await expect(page.locator('#block-demands')).toBeVisible();
    await expect.poll(async()=>{const streams=await(await context.request.get(base+'/__realtime/streams')).json();return streams.websocket+streams.sse;}).toBe(pages.length*(view==='blocks'?2:1));
    if(i<2)continue;
    const before=await probe(pages[0]);result.probes.push({pages:pages.length,...before});
    if(!before.ok){
      await page.close();result.afterClose=await probe(pages[0]);
      expect(before.ok,'ordinary request must reach Console while six subscribed pages remain visible').toBe(true);
    }
  }
  result.visible=await Promise.all(pages.map(page=>page.evaluate(()=>!document.hidden)));
  expect(result.visible).toEqual(Array(6).fill(true));
  result.streams=await (await context.request.get(base+'/__realtime/streams')).json();
  expect(result.streams).toEqual({websocket:view==='blocks'?12:6,sse:0});
  if(view==='network'){
    await pages[0].getByRole('button',{name:'开始连通性检测',exact:true}).click();
    for(const page of pages)await expect(page.getByRole('button',{name:'停止并放弃连通性检测',exact:true})).toBeEnabled();
    await pages[1].getByRole('button',{name:'停止并放弃连通性检测',exact:true}).click();
    for(const page of pages)await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeEnabled();
  }
  await Promise.all(pages.map(page=>page.close()));
  await expect.poll(async()=>await (await context.request.get(base+'/__realtime/streams')).json()).toEqual({websocket:0,sse:0});
  result.released=true;
  expect(result.errors).toEqual([]);
  result.passed=true;
} finally {
  await writeFile(join(output,'result.json'),JSON.stringify(result,null,2));
  await browser.close();
}
