import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [baseURL,key,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}});
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto(baseURL+'/#/timeline');
  const token=await page.locator('meta[name="swd-token"]').getAttribute('content');
  const latency=await (await page.request.get(baseURL+'/api/latency',{headers:{'X-SWD-Token':token}})).json();
  expect(latency.blocking.samples).toBeGreaterThan(0);
  expect(latency.blocking.samples+latency.ready.samples).toBe(601);
  const sources=await (await page.request.get(baseURL+'/api/response-sources',{headers:{'X-SWD-Token':token}})).json();
  const blockBytes=Buffer.byteLength('history browser video');
  expect(sources.downloadBytes+sources.cacheBytes+sources.localBytes).toBe(601*blockBytes);
  expect(sources.localBytes).toBeGreaterThan(0);
  const firstPage=await (await page.request.get(baseURL+'/api/history?limit=50',{headers:{'X-SWD-Token':token}})).json();
  expect(firstPage.requests.every(r=>r.bytes===4&&r.sources.localBytes===blockBytes)).toBe(true);
  await expect(page.getByRole('region',{name:'响应来源统计'})).toContainText('本地视频');
  const history=page.getByRole('table',{name:'请求历史列表'});
  await expect(history.getByRole('link',{name:'ID 42',exact:true})).toHaveCount(50);
  await expect(history.getByRole('link',{name:key,exact:true})).toHaveCount(50);
  await expect(history.getByText('100%',{exact:true})).toHaveCount(50);
  await page.screenshot({path:join(output,'history-desktop.png'),fullPage:false});
  for(let n=2;n<=13;n++){
    await page.getByRole('button',{name:'下一页历史',exact:true}).click();
    await expect(page.getByText(`第 ${n} 页 · ${n===13?1:50} 条`,{exact:true})).toBeVisible();
  }
  await expect(page.getByRole('button',{name:'下一页历史',exact:true})).toBeDisabled();
  await page.setViewportSize({width:390,height:844});await page.emulateMedia({colorScheme:'dark'});
  await page.evaluate(()=>scrollTo(0,0));
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,'history-mobile.png'),fullPage:true});
  await page.reload();await expect(history.getByRole('link',{name:'ID 42',exact:true})).toHaveCount(50);
  expect(errors).toEqual([]);
  await writeFile(join(output,'results.json'),JSON.stringify({records:601,lastPage:13,resource:key,latency,sources,errors},null,2));
} finally {await browser.close();}
