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
  await expect(page.getByText('请求歌曲：ID 42',{exact:true})).toHaveCount(50);
  await expect(page.getByText('资源：'+key,{exact:true})).toHaveCount(50);
  await expect(page.getByText(/交付：100%/)).toHaveCount(50);
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
  await page.reload();await expect(page.getByText('请求歌曲：ID 42',{exact:true})).toHaveCount(50);
  expect(errors).toEqual([]);
  await writeFile(join(output,'results.json'),JSON.stringify({records:601,lastPage:13,resource:key,latency,errors},null,2));
} finally {await browser.close();}
