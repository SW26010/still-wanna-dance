import {chromium,expect} from '@playwright/test';
import {mkdir,readFile,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,source,fixture,output]=process.argv.slice(2);
const content=await readFile(fixture);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.platform==='win32'||!process.env.CI?'msedge':'chromium'});
try {
  const page=await browser.newPage(),errors=[],imports=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',r=>{if(new URL(r.url()).pathname==='/api/proxy/import')imports.push(r.headers()['x-swd-import-source']);});
  await page.goto(base+'/#network-proxy');
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  const inspect=page.getByRole('button',{name:'检查并导入到草稿',exact:true});
  const report=page.getByText('配置检查通过：1 个内置节点，1 个原有代理组。',{exact:true});
  const clear=page.getByRole('button',{name:'清除本次导入草稿',exact:true});
  await page.getByLabel('选择配置文件',{exact:true}).setInputFiles(fixture);
  await inspect.click();await expect(report).toBeVisible();
  await clear.click();await expect(report).toHaveCount(0);
  await page.getByRole('combobox',{name:'配置来源',exact:true}).selectOption('base64');
  await page.getByRole('textbox',{name:'Base64 配置',exact:true}).fill(content.toString('base64'));
  await inspect.click();await expect(report).toBeVisible();
  await clear.click();await expect(report).toHaveCount(0);
  await page.getByRole('combobox',{name:'配置来源',exact:true}).selectOption('url');
  const address=page.getByRole('textbox',{name:'配置 URL',exact:true});
  await address.fill(source);
  await inspect.click();await expect(report).toBeVisible();
  await expect(address).toHaveValue('');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('region',{name:'设置操作',exact:true}).getByRole('status')).toHaveText('待重启');
  await page.reload();
  await expect(page.getByRole('combobox',{name:'配置来源',exact:true})).toHaveValue('config');
  await expect(page.getByRole('textbox',{name:'配置内容',exact:true})).toHaveValue('');
  expect(imports).toEqual(['config','base64','url']);
  expect(errors).toEqual([]);
  for(const width of [390,1280]) {
    await page.setViewportSize({width,height:900});
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:join(output,`saved-${width}.png`),fullPage:true});
  }
  await writeFile(join(output,'result.json'),JSON.stringify({passed:true,sources:imports,pageErrors:errors,sourceCleared:true,pendingRestart:true},null,2));
  console.log('Real Console: file/Base64/URL native validation, original encrypted save, cleared source and reload passed.');
} finally {await browser.close();}
