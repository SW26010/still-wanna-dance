import {launchBrowser} from './browser.mjs';
import {expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await launchBrowser();
try {
  const page=await browser.newPage(),errors=[],posts=[];
  page.on('pageerror',error=>errors.push(error.message));
  page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname==='/api/settings')posts.push(r.postDataJSON());});
  await page.goto(base+'/#network-proxy');
  const token=await page.locator('meta[name="swd-token"]').getAttribute('content');
  const state=async()=>{const r=await page.request.get(base+'/api/status',{headers:{'X-SWD-Token':token}});expect(r.ok()).toBe(true);return r.json();};
  const initial=await state();
  await page.setViewportSize({width:320,height:900});
  await page.goto(base+'/#download-settings');
  for(const [label,description] of [
    ['同时下载数量','普通下载的并发上限；播放正在等待的数据可突破此限制。'],
    ['并发增长间隔','有下载积压时，每隔此时间最多增加一路并发；设为 0 时按需直接使用并发上限。'],
    ['提前准备时间','提前保护预计即将使用的视频数据，并安排下载缺失的部分。'],
    ['延续保护时间','需求结束后继续保护缓存的时间；设为 0 时不延续保护。'],
  ]) {
    await page.getByText(label,{exact:true}).hover();await expect(page.getByRole('tooltip')).toHaveText(description);
    await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toHaveCount(0);
    await page.mouse.move(0,0);
  }
  const limit=page.getByRole('spinbutton',{name:'同时下载数量',exact:true});
  await limit.focus();await expect(page.getByRole('tooltip')).toHaveText('普通下载的并发上限；播放正在等待的数据可突破此限制。');
  await limit.fill('65');await page.getByRole('button',{name:'保存设置',exact:true}).focus();
  await expect(limit).toHaveAttribute('aria-invalid','true');
  await expect(limit).toHaveAccessibleDescription(/请输入 1～64 范围内的数值/);
  const numericError=page.getByRole('button',{name:'同时下载数量：错误',exact:true});
  await page.getByText('同时下载数量',{exact:true}).hover();await expect(page.getByRole('tooltip')).toHaveText('普通下载的并发上限；播放正在等待的数据可突破此限制。');
  await numericError.hover();await expect(page.getByRole('tooltip')).toHaveText('请输入 1～64 范围内的数值');
  await numericError.click();await expect(page.getByRole('tooltip')).toHaveText('请输入 1～64 范围内的数值');
  await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toHaveCount(0);
  await page.mouse.move(0,0);await limit.focus();await page.keyboard.press('Tab');
  await expect(numericError).toBeFocused();await expect(page.getByRole('tooltip')).toHaveText('请输入 1～64 范围内的数值');
  await page.screenshot({path:join(output,'numeric-error.png'),fullPage:true});
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  expect(posts).toHaveLength(0);
  expect((await state()).settings).toEqual(initial.settings);
  await page.getByRole('button',{name:'放弃更改',exact:true}).click();
  await page.setViewportSize({width:1280,height:900});
  await page.goto(base+'/#network-proxy');
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  const textarea=page.getByRole('textbox',{name:'导入配置文件',exact:true});
  const before=await textarea.boundingBox();
  await save.click();
  await expect(textarea).toHaveAttribute('aria-invalid','true');
  const error=page.getByRole('button',{name:'导入配置文件：错误',exact:true});
  await error.hover();await expect(page.getByRole('tooltip')).toContainText('请先导入有效的 Mihomo 配置');
  expect(await textarea.boundingBox()).toEqual(before);
  expect((await state()).settings).toEqual(initial.settings);
  await page.screenshot({path:join(output,'mihomo-rejected.png'),fullPage:true});
  await textarea.focus();await save.focus();
  await expect(error).toBeVisible();
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('external-socks5');
  const address=page.getByRole('textbox',{name:'SOCKS5 地址',exact:true});
  await address.fill('%%%:7891');await save.click();
  await expect(address).toHaveAttribute('aria-invalid','true');
  expect(posts).toHaveLength(1);
  await address.fill('localhost:7891');await save.click();
  await expect(page.getByRole('region',{name:'设置操作',exact:true}).getByRole('status')).toHaveText('待重启');
  const final=await state();
  expect(final.settings.socks5Address).toBe('localhost:7891');
  expect(final.activeSettings).toEqual(initial.activeSettings);
  expect(final.actionErrors?.settings).toBeUndefined();
  expect(errors).toEqual([]);
  await writeFile(join(output,'result.json'),JSON.stringify({passed:true,numericTooltipMouseAndKeyboard:true,rejectedWithoutPersistence:true,activeUnchanged:true,posts:posts.length,pageErrors:errors},null,2));
  console.log('Real Console: numeric hint/error mouse and keyboard triggers, missing nodes rejected without persistence, format validation and valid save passed.');
}finally{await browser.close();}
