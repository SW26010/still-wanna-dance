import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:'msedge'});
try {
  const page=await browser.newPage(),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  const delivery=async()=>{const r=await page.request.get(base+'/test/fault-openings');expect(r.ok()).toBe(true);return r.json();};
  await page.goto(base+'/#/settings');
  await expect.poll(async()=>(await delivery()).connected).toBe(true);
  // A background page also has a lifecycle subscription. A settings fault
  // must preserve both pages and the unsaved draft.
  const second=await browser.newPage();
  await second.goto(base+'/#/about');
  const level=page.getByLabel('程序日志级别',{exact:true});
  await level.selectOption('debug');
  const save=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/settings'&&r.request().method()==='POST');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  const failedSave=await save;
  expect(failedSave.status()).toBe(400);
  const {error}=await failedSave.json();
  await expect(page.locator('#preferences').getByText(error,{exact:true})).toBeVisible();
  await expect.poll(async()=>(await delivery()).checks).toBeGreaterThan(0);
  expect((await delivery()).openings).toBe(0);
  await expect(page).toHaveURL(base+'/#/settings');
  await expect(level).toHaveValue('debug');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  await page.screenshot({path:join(output,'settings-write-failure.png'),fullPage:true});

  await expect(second).toHaveURL(base+'/#/about');
  await expect(page).toHaveURL(base+'/#/settings');
  await expect(level).toHaveValue('debug');
  expect(errors).toEqual([]);
  await writeFile(join(output,'result.json'),JSON.stringify({passed:true,saveFailureVisible:true,draftRetained:true,multiplePagesRetained:true,openings:0,pageErrors:errors},null,2));
  console.log('Real Console + Edge: settings write error retained both routes, draft and feedback without opening home.');
} finally {await browser.close();}
