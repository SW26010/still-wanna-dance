import {chromium,expect} from '@playwright/test';
import {mkdir,readFile,writeFile} from 'node:fs/promises';
import {basename,join} from 'node:path';

const [base,source,fixture,output]=process.argv.slice(2);
const content=await readFile(fixture);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.platform==='win32'||!process.env.CI?'msedge':'chromium'});
try {
  const page=await browser.newPage(),errors=[],imports=[],posts=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',r=>{
    const path=new URL(r.url()).pathname;
    if(path==='/api/proxy/import')imports.push(r.headers()['x-swd-import-source']);
    if(path==='/api/settings')posts.push(r.postDataJSON());
  });
  await page.goto(base+'/#network-proxy');
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  const input=page.getByRole('textbox',{name:'导入配置文件',exact:true});
  const current=page.getByRole('textbox',{name:'当前配置文件',exact:true});
  const method=page.getByRole('combobox',{name:'配置来源',exact:true});
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  const state=page.getByRole('region',{name:'设置操作',exact:true}).getByRole('status');
  const saveSettings=async()=>{
    const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/settings'&&r.request().method()==='POST');
    await save.click();expect((await response).ok()).toBe(true);
    await expect(state).toHaveText('待重启');
  };
  await expect(current).toHaveValue('未导入配置');await expect(input).toHaveAttribute('readonly','');
  const chooser=page.waitForEvent('filechooser');await input.click();await (await chooser).setFiles(fixture);
  await expect(input).toHaveValue(basename(fixture));expect(imports).toHaveLength(0);
  await saveSettings();
  await expect.poll(()=>current.inputValue()).toContain(basename(fixture));
  const fileSummary=await current.inputValue();
  expect(fileSummary).toContain('1 个节点');expect(fileSummary).toContain('1 个代理组');expect(fileSummary).toContain('导入于 ');
  await page.reload();await expect(current).toHaveValue(fileSummary);

  await method.selectOption('base64');await input.fill(content.toString('base64'));
  await page.getByRole('button',{name:'导入',exact:true}).click();
  await expect(current).toHaveValue(/Base64 配置.*1 个节点.*1 个代理组/);
  const base64Summary=await current.inputValue();await saveSettings();
  expect(imports).toEqual(['config','base64']);

  await method.selectOption('url');
  const invalid=new URL(source);invalid.pathname='/invalid';
  await input.fill(invalid.href);await save.click();
  await expect(input).toHaveAttribute('aria-invalid','true');
  await expect(current).toHaveValue(base64Summary);expect(posts).toHaveLength(2);
  await input.fill(source);await saveSettings();
  await expect(input).toHaveValue('');await expect(current).toHaveValue(/URL 配置.*1 个节点.*1 个代理组/);
  const finalSummary=await current.inputValue();
  await page.reload();await expect(current).toHaveValue(finalSummary);await expect(method).toHaveValue('config');await expect(input).toHaveValue('');
  expect(imports).toEqual(['config','base64','url','url']);expect(posts).toHaveLength(3);
  for(const body of posts)for(const name of ['proxyImportText','proxyImportSource','proxyImportName'])expect(body).not.toHaveProperty(name);
  expect(errors).toEqual([]);
  for(const width of [320,390,1280]){
    await page.setViewportSize({width,height:900});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:join(output,'saved-'+width+'.png'),fullPage:true});
  }
  await writeFile(join(output,'result.json'),JSON.stringify({passed:true,sources:imports,saves:posts.length,pageErrors:errors,summarySurvivedReload:true,invalidImportDidNotSave:true},null,2));
  console.log('Real Console: file picker, metadata reload, automatic file/URL validation, explicit Base64 reuse and invalid-import preservation passed.');
}finally{await browser.close();}
