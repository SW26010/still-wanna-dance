import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [baseURL,phase,active,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:950}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(baseURL+'/#/settings');
  const level=page.getByRole('combobox',{name:'程序日志级别',exact:true});
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  const restart=page.getByRole('button',{name:'重启并应用',exact:true});
  await expect(level).toHaveValue(active);
  await expect(restart).toBeDisabled();
  if(phase!=='reopen') {
    await level.selectOption(phase);await save.click();
    await expect(save).toBeDisabled();
    await expect(restart).toBeEnabled();
    await page.reload();await expect(level).toHaveValue(phase);
  }
  const token=await page.locator('meta[name="swd-token"]').getAttribute('content');
  const response=await page.request.get(baseURL+'/api/status',{headers:{'X-SWD-Token':token}});
  expect(response.ok()).toBe(true);
  const state=await response.json();
  expect(state.activeSettings.logLevel).toBe(active);
  expect(state.settings.logLevel).toBe(phase==='reopen'?'info':phase);
  expect(state.restartRequired).toBe(phase!=='reopen');
  expect(state.running).toBe(false);
  await page.setViewportSize({width:390,height:844});
  await level.scrollIntoViewIfNeeded();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,`logging-${phase}.png`)});
  expect(errors).toEqual([]);
  await writeFile(join(output,`browser-${phase}.json`),JSON.stringify({passed:true,active:state.activeSettings.logLevel,saved:state.settings.logLevel,restartRequired:state.restartRequired,errors},null,2));
  console.log(`Real Console logging settings ${phase} checks passed.`);
} finally {await browser.close();}
