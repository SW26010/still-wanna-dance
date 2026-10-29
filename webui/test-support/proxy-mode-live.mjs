import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [baseURL,phase,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(baseURL+'/#network-proxy');
  const mode=page.getByRole('combobox',{name:'上游连接',exact:true});
  const notice=page.getByText('上游连接已改为代理',{exact:true});
  await expect(mode).toHaveValue('proxy');
  await expect(mode.locator('option')).toHaveText(['直连（内置 DNS）','代理']);
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
  await expect(notice).toHaveCount(0);
  await page.getByRole('link',{name:'加速',exact:true}).click();
  await expect(page.getByRole('heading',{name:'加速',level:1,exact:true})).toBeVisible();
  if(phase==='migrated') {
    await expect(notice).toBeVisible();
    await page.getByRole('link',{name:'网络设置',exact:true}).click();
    await expect(mode).toHaveValue('proxy');
    await page.goto(baseURL+'/#/home');
    await expect(page.getByRole('heading',{name:'加速',level:1,exact:true})).toBeVisible();
  }
  await expect(notice).toHaveCount(0);
  const token=await page.locator('meta[name="swd-token"]').getAttribute('content');
  const response=await page.request.get(baseURL+'/api/status',{headers:{'X-SWD-Token':token}});
  expect(response.ok()).toBe(true);
  const state=await response.json();
  expect(state.settings.upstreamMode).toBe('proxy');
  expect(state.activeSettings.upstreamMode).toBe('proxy');
  expect(state.restartRequired).toBe(false);
  expect(state.running).toBe(false);
  expect(!!state.proxyModeMigrated).toBe(phase==='migrated');
  if(phase==='migrated') {
    // Each screenshot gets fresh session storage; finishing Toast animations can dismiss it.
    for(const width of [1280,320]) {
      const visualPage=await browser.newPage({viewport:{width,height:900}});
      visualPage.on('pageerror',error=>errors.push(error.message));
      await visualPage.goto(baseURL+'/#/home');
      const visualNotice=visualPage.getByText('上游连接已改为代理',{exact:true});
      await expect(visualNotice).toBeInViewport();
      await visualNotice.hover();
      expect(await visualPage.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
      await visualPage.screenshot({path:join(output,`migration-${width}.png`),animations:'disabled'});
      await visualPage.close();
    }
  }
  expect(errors).toEqual([]);
  await writeFile(join(output,`browser-${phase}.json`),JSON.stringify({passed:true,mode:state.activeSettings.upstreamMode,notice:!!state.proxyModeMigrated,errors},null,2));
  console.log(`Real Console proxy mode ${phase} checks passed.`);
} finally {await browser.close();}
