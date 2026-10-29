import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,phase,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:900}}),errors=[],actions=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{if(request.method()==='POST')actions.push(new URL(request.url()).pathname);});
  await page.goto(base+'/#video-import');
  const panel=page.locator('#video-import');
  await expect(panel.getByRole('textbox',{name:'外部目录',exact:true})).not.toHaveValue('');
  await expect(panel.getByRole('textbox',{name:'外部目录',exact:true})).toBeDisabled();
  await expect(panel.getByText('预计不额外占用视频空间',{exact:true})).toBeVisible();
  if(phase==='resume') {
    await expect(panel.getByRole('status')).toContainText('导入已暂停');
    await page.reload();
    await expect(panel.getByRole('button',{name:'继续导入',exact:true})).toBeEnabled();
    expect(actions.filter(path=>path.startsWith('/api/import/'))).toEqual([]);
    await panel.screenshot({path:join(output,'paused.png')});
    await panel.getByRole('button',{name:'继续导入',exact:true}).click();
    await expect(panel.getByRole('status')).toContainText('已导入 2',{timeout:10000});
    await expect(panel.getByRole('status')).toContainText('导入完成');
    await expect(panel.getByRole('button',{name:'清除已导入的源文件',exact:true})).toBeDisabled();
  } else {
    await expect(panel.getByRole('status')).toContainText('已导入 2');
    expect(actions.filter(path=>path.startsWith('/api/import/'))).toEqual([]);
    await panel.getByRole('checkbox',{name:/清除原位置本次成功导入/}).check();
    await panel.getByRole('button',{name:'清除已导入的源文件',exact:true}).click();
    await page.getByRole('dialog').getByRole('button',{name:'取消',exact:true}).click();
    expect(actions).not.toContain('/api/import/cleanup');
    await panel.getByRole('button',{name:'清除已导入的源文件',exact:true}).click();
    await page.getByRole('dialog').getByRole('button',{name:'确认',exact:true}).click();
    await expect(panel.getByRole('status')).toContainText('已清除 2',{timeout:10000});
    await panel.screenshot({path:join(output,'cleaned.png')});
    await panel.getByRole('button',{name:'结束本次',exact:true}).click();
    await page.getByRole('dialog').getByRole('button',{name:'确认',exact:true}).click();
    await expect(panel.getByRole('status')).toContainText('尚未开始');
    await page.reload();
    await expect(panel.getByRole('button',{name:'扫描并导入',exact:true})).toBeVisible();
  }
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  expect(errors).toEqual([]);
  await writeFile(join(output,phase+'.json'),JSON.stringify({phase,actions,errors},null,2));
  console.log(`Real Console import ${phase}: persistence, explicit actions and narrow layout passed.`);
} finally {await browser.close();}
