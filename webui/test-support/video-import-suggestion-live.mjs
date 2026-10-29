import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,phase,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const context=await browser.newContext({viewport:{width:390,height:900}});
  const page=await context.newPage(),errors=[],actions=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{if(request.method()==='POST')actions.push(new URL(request.url()).pathname);});
  await page.goto(base);
  const notice=page.getByRole('listitem',{name:'已有本地视频？',exact:true});
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeVisible();
  if(phase==='first-run') {
    await expect(notice).toBeVisible();
    await page.screenshot({path:join(output,'first-run.png'),animations:'disabled'});
    await notice.getByRole('link',{name:'前往导入',exact:true}).click();
    await expect(page.locator('#video-import')).toBeFocused();
    await expect(page.getByRole('textbox',{name:'外部目录',exact:true})).toHaveValue('');
    await expect(notice).toHaveCount(0);
    await page.reload();
    await expect(page.getByRole('textbox',{name:'外部目录',exact:true})).toBeVisible();
    expect(actions.filter(path=>path.startsWith('/api/import/'))).toEqual(['/api/import/dismiss-suggestion']);
  }
  await expect(notice).toHaveCount(0);
  // A new tab also reads the shared acknowledgement, without browser storage.
  const other=await page.context().newPage();
  await other.goto(base);
  await expect(other.getByRole('button',{name:'开始加速',exact:true})).toBeVisible();
  await expect(other.getByRole('listitem',{name:'已有本地视频？',exact:true})).toHaveCount(0);
  expect(actions.some(path=>path==='/api/path/select'||path.startsWith('/api/activation/'))).toBe(false);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  expect(errors).toEqual([]);
  await page.screenshot({path:join(output,phase+'-handled.png'),animations:'disabled'});
  await writeFile(join(output,phase+'.json'),JSON.stringify({phase,actions,errors},null,2));
  console.log(`${phase}: suggestion, navigation, refresh and shared acknowledgement verified`);
} finally {await browser.close();}
