import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
const [base,output,phase]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:900}}),errors=[],mutations=[],reads=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',request=>{const path=new URL(request.url()).pathname;if(request.method()==='POST')mutations.push(path);else reads.push(path);});
  await page.setViewportSize({width:1280,height:900});
  await page.goto(base+'/#/library');
  if(phase==='confirmation') {
    await expect(page.getByRole('group',{name:'待确认存储',exact:true})).toBeVisible();
    await expect(page.getByRole('button',{name:'检查本地视频',exact:true})).toBeDisabled();
    await expect(page.getByRole('button',{name:'更新曲库',exact:true})).toBeDisabled();
    await expect(page.getByText('曲库读取失败，请刷新重试。',{exact:true})).toHaveCount(0);
    await expect(page.getByText('正在读取曲库…',{exact:true})).toHaveCount(0);
    await expect(page.getByText(/无法读取本地清单状态/)).toHaveCount(0);
    expect(reads.filter(path=>path==='/api/library'||path==='/api/inventory')).toEqual([]);
  } else if(phase==='failure') {
    await expect(page.getByText('曲库读取失败，请刷新重试。',{exact:true})).toBeVisible();
    await expect(page.locator('.fui-ToastTitle').filter({hasText:'无法读取本地清单状态'})).toBeVisible();
  } else {
    await expect(page.getByRole('button',{name:'检查本地视频',exact:true})).toBeEnabled();
    await expect(page.getByText('曲库读取失败，请刷新重试。',{exact:true})).toHaveCount(0);
    expect(reads).toContain('/api/library');
  }
  await page.screenshot({path:join(output,'library-'+phase+'.png'),fullPage:true});
  await page.setViewportSize({width:390,height:900});
  await page.goto(base+'/#storage-settings');
  const badge=page.getByRole('status',{name:'后台程序',exact:true});
  if(phase==='reopened'){
    await expect(badge).toHaveText('后台程序：已连接');
    await expect(page.getByRole('button',{name:/数据库文件：(?:错误|警告)/})).toHaveCount(0);
    await expect(page.getByRole('group',{name:'待确认存储',exact:true})).toHaveCount(0);
  }else{
    const warning=phase==='confirmation';
    await expect(badge).toHaveText(warning?'后台程序：待确认存储':'后台程序：存储故障');
    const icon=page.getByRole('button',{name:warning?'数据库文件：警告':'数据库文件：错误',exact:true});
    await icon.focus();
    await expect(page.getByRole('tooltip')).toContainText(warning?'尚未指定':'数据库不可用');
    await page.screenshot({path:join(output,'field-before.png'),fullPage:true});
    await page.keyboard.press('Escape');
    if(warning){
      await page.getByRole('group',{name:'待确认存储',exact:true}).getByRole('button',{name:'确认并保存',exact:true}).click();
    }else{
      await page.getByRole('textbox',{name:'数据库文件',exact:true}).click();
      await expect(page.getByText('已选择数据库。保存设置并重启后使用。',{exact:true})).toBeVisible();
      await page.getByRole('button',{name:'保存设置',exact:true}).click();
    }
    const policy=page.getByRole('dialog',{name:'确认此目录的视频保留方式',exact:true});
    await expect(policy).toContainText('不限制视频空间');
    await policy.getByRole('button',{name:'取消',exact:true}).click();
    expect(mutations).not.toContain('/api/settings');
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await policy.getByRole('button',{name:'确认并保存',exact:true}).click();
    await expect(page.getByRole('region',{name:'设置操作',exact:true}).getByRole('status')).toHaveText('待重启');
    await expect(icon).toHaveCount(0);
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
    if(warning)expect(mutations).not.toContain('/api/path/select');
  }
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,phase+'.png'),fullPage:true});
  expect(errors).toEqual([]);
  await writeFile(join(output,phase+'.json'),JSON.stringify({passed:true,mutations,errors},null,2));
}finally{await browser.close();}
