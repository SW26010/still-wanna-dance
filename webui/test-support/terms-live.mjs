import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [baseURL,phase,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:950}}),errors=[],requests=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{const path=new URL(request.url()).pathname;if(path.startsWith('/api/'))requests.push(path);});
  await page.goto(baseURL);
  const dialog=page.getByRole('alertdialog',{name:'使用条款'});
  if(phase==='accept'){
    await expect(dialog).toBeVisible();await expect(dialog.getByRole('article')).not.toBeEmpty();
    expect(requests).not.toContain('/api/status');
    await page.keyboard.press('Escape');await expect(dialog).toBeVisible();
    await page.screenshot({path:join(output,'terms-desktop.png'),fullPage:true,animations:'disabled'});
    await page.setViewportSize({width:390,height:844});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:join(output,'terms-mobile.png'),fullPage:true,animations:'disabled'});
    await dialog.getByRole('button',{name:'同意完整条款'}).click();
  }
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeVisible();
  await page.reload();await expect(dialog).toHaveCount(0);
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeVisible();
  expect(requests.some(path=>path.startsWith('/api/setup'))).toBe(false);
  expect(requests.filter(path=>path==='/api/terms/accept')).toHaveLength(phase==='accept'?1:0);
  expect(errors).toEqual([]);
  await writeFile(join(output,`terms-${phase}.json`),JSON.stringify({passed:true,errors,requests},null,2));
  console.log(`Real Console consent ${phase} passed.`);
} finally {await browser.close();}
