import {test,expect} from '@playwright/test';
import {mockAPI,choosePath} from './api';

test('SOCKS5 validation uses a stable Fluent icon and tooltip without native bubbles',async({page})=>{
  const api=await mockAPI(page);
  await page.goto('/#network-proxy');
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  const address=page.getByRole('textbox',{name:'SOCKS5 地址',exact:true});
  const username=page.getByRole('textbox',{name:'SOCKS5 用户名（可选）',exact:true});
  const before=await username.boundingBox();
  await address.focus();await username.focus();
  await expect(address).toHaveAttribute('aria-invalid','true');
  const icon=page.getByRole('button',{name:'SOCKS5 地址：错误',exact:true});
  await expect(icon).toBeVisible();
  await expect(page.getByRole('tooltip')).toHaveCount(0);
  await icon.hover();
  await expect(page.getByRole('tooltip')).toContainText('请输入');
  expect(await username.boundingBox()).toEqual(before);
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  for(const value of ['%%%:7891','[garbage]:7891','127.0.0.1:+7891','999.999.999.999:7891']) {
    await address.fill(value);await expect(address).toHaveAttribute('aria-invalid','true');
  }
  await address.fill('[::1]:7891');
  await expect(address).not.toHaveAttribute('aria-invalid','true');
  await expect(icon).toHaveCount(0);
  expect(await username.boundingBox()).toEqual(before);
});

test('directory conflicts highlight the read-only field without inserting error text',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/storage/validate-location',r=>r.fulfill({json:{valid:false,field:'video',message:'目录不能互相包含'}}));
  await page.goto('/#storage-settings');
  const video=page.getByRole('textbox',{name:'完整视频目录',exact:true});
  const before=await page.getByRole('radiogroup').first().boundingBox();
  await choosePath(page,'完整视频目录','D:\\Conflicting');
  await expect(video).toHaveAttribute('aria-invalid','true');
  const icon=page.getByRole('button',{name:'完整视频目录：错误',exact:true});
  await expect(icon).toBeVisible();
  expect(await page.getByRole('radiogroup').first().boundingBox()).toEqual(before);
  await icon.focus();await expect(page.getByRole('tooltip')).toContainText('目录不能互相包含');
  await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toBeHidden();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
});

for(const width of [320,1280])for(const theme of ['light','dark','forced'] as const)test(`numeric error icon keeps layout at ${width}px in ${theme}`,async({page},info)=>{
  const api=await mockAPI(page);
  await page.setViewportSize({width,height:900});
  await page.emulateMedia({colorScheme:theme==='dark'?'dark':'light',forcedColors:theme==='forced'?'active':'none'});
  await page.goto('/#download-settings');
  const input=page.getByRole('spinbutton',{name:'同时下载数量',exact:true});
  const interval=page.getByRole('spinbutton',{name:'并发增长间隔（毫秒）',exact:true});
  await input.scrollIntoViewIfNeeded();
  const before=await interval.boundingBox();
  const normalBorder=await input.locator('..').evaluate(e=>getComputedStyle(e).borderColor);
  await input.fill('65');await interval.focus();
  await expect(input).toHaveAttribute('aria-invalid','true');
  if(theme!=='forced')expect(await input.locator('..').evaluate(e=>getComputedStyle(e).borderColor)).not.toBe(normalBorder);
  const icon=page.getByRole('button',{name:'同时下载数量：错误',exact:true});
  await icon.hover();await expect(page.getByRole('tooltip')).toContainText('1～64');
  expect(await interval.boundingBox()).toEqual(before);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('numeric-error.png'),fullPage:true});
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await input.fill('16');await expect(icon).toHaveCount(0);
});

test('server validation keeps the Mihomo draft and marks its field after a failed save',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/settings',r=>r.fulfill({status:400,json:{code:'settings_validation',error:'请先导入至少一个受支持的 Mihomo 节点',fields:{proxyImportText:'请先导入至少一个受支持的 Mihomo 节点'}}}));
  await page.goto('/#network-proxy');
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('textbox',{name:'粘贴节点内容',exact:true})).toHaveAttribute('aria-invalid','true');
  await expect(page).toHaveURL(/#network-proxy$/);
  expect(api.state.settings.upstreamMode).toBe('direct');
  await page.getByRole('button',{name:'粘贴节点内容：错误',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('请先导入至少一个');
  await expect(page.getByRole('combobox',{name:'代理后端',exact:true})).toHaveValue('mihomo');
});
