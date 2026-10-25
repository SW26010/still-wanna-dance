import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const [baseURL,phase,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:950},colorScheme:'light'}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(baseURL+'/#/settings');
  const theme=page.getByLabel('主题',{exact:true}),provider=page.locator('#root > .app-provider');
  if(phase==='change') {
    const other=await browser.newPage({colorScheme:'light'});
    await other.goto(baseURL+'/#/settings');
    await expect(theme).toHaveValue('system');
    await page.getByLabel('启动程序时自动打开 WebUI',{exact:true}).uncheck();
    await theme.selectOption('dark');
    await expect(provider).toHaveAttribute('data-theme','dark');
    await expect(other.locator('#root > .app-provider')).toHaveAttribute('data-theme','dark',{timeout:10000});
    await expect(page.getByLabel('启动程序时自动打开 WebUI',{exact:true})).not.toBeChecked();
    await page.getByRole('button',{name:'放弃更改',exact:true}).click();
    await expect(page.getByLabel('启动程序时自动打开 WebUI',{exact:true})).toBeChecked();
    await expect(theme).toHaveValue('dark');
    await theme.selectOption('light');
    await expect(provider).toHaveAttribute('data-theme','light');
    await expect(page.locator('link[rel="icon"]')).toHaveAttribute('href','/favicon-light.ico');
    await page.screenshot({path:join(output,'theme-light.png'),fullPage:true,animations:'disabled'});
    await theme.selectOption('system');
    await expect(theme).toHaveValue('system');
    await page.emulateMedia({colorScheme:'dark'});
    await expect(provider).toHaveAttribute('data-theme','dark');
    await page.emulateMedia({colorScheme:'light'});
    await expect(provider).toHaveAttribute('data-theme','light');
    await theme.selectOption('dark');
  } else {
    expect(await page.locator('#swd-bootstrap').textContent()).toContain('"theme":"dark"');
  }
  await expect(theme).toHaveValue('dark');
  await expect(provider).toHaveAttribute('data-theme','dark');
  await expect(page.locator('link[rel="icon"]')).toHaveAttribute('href','/favicon.ico');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
  await page.setViewportSize({width:390,height:900});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,`theme-dark-${phase}.png`),fullPage:true,animations:'disabled'});
  expect(errors).toEqual([]);
  await writeFile(join(output,`browser-${phase}.json`),JSON.stringify({passed:true,errors},null,2));
  console.log(`Real Console theme ${phase} checks passed.`);
} finally {await browser.close();}
