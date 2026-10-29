import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:900}}),errors=[],actions=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{if(request.method()==='POST')actions.push(new URL(request.url()).pathname);});
  await page.goto(base+'/#video-import');
  const panel=page.locator('#video-import');
  await expect(panel.getByText('选择来源后显示空间预估',{exact:true})).toBeVisible();
  await panel.getByRole('textbox',{name:'外部目录',exact:true}).click();
  const hint=panel.getByText('预计不额外占用视频空间',{exact:true});
  await expect(hint).toBeVisible();
  await hint.focus();
  await expect(page.getByRole('tooltip')).toContainText('硬链接共享原视频内容');
  await page.keyboard.press('Escape');
  await expect(page.getByRole('tooltip')).toHaveCount(0);
  await panel.screenshot({path:join(output,'real-console.png')});
  await panel.getByRole('button',{name:'外部目录：恢复默认'}).click();
  await expect(panel.getByText('选择来源后显示空间预估',{exact:true})).toBeVisible();
  expect(actions.filter(path=>path.startsWith('/api/import/')||path==='/api/path/select')).toEqual(['/api/path/select']);
  expect(errors).toEqual([]);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await writeFile(join(output,'real-console.json'),JSON.stringify({actions,errors},null,2));
  console.log('Real Console path selection, same-volume preview, keyboard tooltip and reset passed without starting an import.');
} finally {await browser.close();}
