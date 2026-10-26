import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
const [base,databasePath,videoDir,output,phase]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:900}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto(base+'/#storage-settings');
  const db=page.getByRole('textbox',{name:'数据库文件',exact:true}),videos=page.getByRole('textbox',{name:'完整视频目录',exact:true});
  if(phase==='configure') {
    await expect(db).not.toHaveValue('');
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
    await db.fill(databasePath);
    await page.getByRole('button',{name:'在此新建数据库',exact:true}).click();
    await expect(db).not.toHaveValue('');
    await expect(page.getByText('数据库已新建。保存设置并重启后使用。',{exact:true})).toBeVisible();
    await videos.fill(videoDir);
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  } else {
    await expect(videos).toHaveValue(videoDir);
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
    await expect(page.getByText('当前数据库：尚未指定',{exact:false})).toHaveCount(0);
    await page.getByRole('button',{name:'在此新建数据库',exact:true}).click();
    await expect(page.getByText(/数据库文件已存在/)).toBeVisible();
  }
  await expect(page.getByText('正在处理…',{exact:true})).toHaveCount(0);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,phase+'.png'),fullPage:true});
  expect(errors).toEqual([]);
  await writeFile(join(output,phase+'.json'),JSON.stringify({phase,database:await db.inputValue(),videos:await videos.inputValue(),errors},null,2));
} finally { await browser.close(); }
