import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.platform==='win32'?'msedge':'chromium'});
const result={browser:browser.version(),realConsole:true,errors:[]};
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}});
  page.on('pageerror',error=>result.errors.push(error.message));
  await page.goto(base+'/#/library');
  const table=page.getByRole('table',{name:'批量任务失败记录',exact:true});
  await expect(table.locator('tbody tr')).toHaveCount(50);
  result.libraryRows=await table.locator('tbody tr').count();
  const details=await page.evaluate(async()=>{
    const token=document.querySelector('meta[name="swd-token"]').content;
    const response=await fetch('/api/status',{headers:{'X-SWD-Token':token}}),text=await response.text(),status=JSON.parse(text);
    return {statusBytes:new TextEncoder().encode(text).length,failed:status.batch.failed,containsFailures:'failures' in status.batch};
  });
  Object.assign(result,details);
  expect(details.failed).toBe(10575);
  expect(details.containsFailures).toBe(false);
  expect(details.statusBytes).toBeLessThan(20000);
  await page.getByRole('button',{name:'下一页批量任务失败记录',exact:true}).click();
  await expect(table.locator('tbody tr').first()).toContainText('51 · 失败歌曲 51');
  await page.locator('#batch').screenshot({path:join(output,'failures-page-2.png')});
  const navigation=page.getByRole('navigation',{name:'页面导航',exact:true});
  await navigation.getByRole('link',{name:'加速',exact:true}).click();
  await expect(page.locator('table[aria-label="批量任务失败记录"]')).toHaveCount(0);
  result.homeRows=await page.locator('table[aria-label="批量任务失败记录"] tbody tr').count();
  await navigation.getByRole('link',{name:'曲库',exact:true}).click();
  await expect(table.locator('tbody tr')).toHaveCount(50);
  await expect(table.locator('tbody tr').first()).toContainText('1 · 失败歌曲 1');
  await page.setViewportSize({width:390,height:844});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.locator('#batch').screenshot({path:join(output,'failures-mobile.png')});
  expect(result.errors).toEqual([]);
  result.passed=true;
} finally {
  await writeFile(join(output,'result.json'),JSON.stringify(result,null,2));
  await browser.close();
}
