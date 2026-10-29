import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [baseURL,output,videoDir]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const first=await browser.newPage(),second=await browser.newPage(),errors=[];
  for(const page of [first,second])page.on('pageerror',error=>errors.push(error.message));
  await first.goto(baseURL+'/#/settings');
  await expect(first.getByLabel('程序日志级别',{exact:true})).toHaveValue('info');
  const token=await first.locator('meta[name="swd-token"]').getAttribute('content');
  const status=async()=>{
    const response=await first.request.get(baseURL+'/api/status',{headers:{'X-SWD-Token':token}});
    expect(response.ok()).toBe(true);return response.json();
  };
  const initial=await status();let stale=true,submitted;
  // Hold only the second page's polling snapshot; both saves reach the real Console.
  await second.route('**/api/status',route=>stale?route.fulfill({json:initial}):route.continue());
  second.on('request',request=>{
    if(new URL(request.url()).pathname==='/api/settings'&&request.method()==='POST'){
      submitted=request.postDataJSON();stale=false;
    }
  });
  await second.goto(baseURL+'/#prepareAheadSeconds');
  await second.getByRole('spinbutton',{name:'同时下载数量',exact:true}).fill('17');
  await first.getByLabel('程序日志级别',{exact:true}).selectOption('warn');
  await first.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(async()=>(await status()).settings.logLevel).toBe('warn');
  await second.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(async()=>(await status()).settings.downloadConcurrencySoftLimit).toBe(17);
  const saved=await status();
  expect(saved.settings.logLevel).toBe('warn');
  expect(submitted).toEqual({downloadConcurrencySoftLimit:17});
  expect(saved.activeSettings.logLevel).toBe('info');
  expect(saved.activeSettings.downloadConcurrencySoftLimit).toBe(initial.settings.downloadConcurrencySoftLimit);
  expect(saved.restartRequired).toBe(true);
  await second.reload();
  await expect(second.getByRole('spinbutton',{name:'同时下载数量',exact:true})).toHaveValue('17');
  await second.getByRole('tab',{name:'常规',exact:true}).click();
  await expect(second.getByLabel('程序日志级别',{exact:true})).toHaveValue('warn');
  await second.getByRole('tab',{name:'存储',exact:true}).click();
  await second.route('**/api/path/select',route=>route.fulfill({json:{path:videoDir,canceled:false}}));
  await second.getByRole('textbox',{name:'完整视频目录',exact:true}).click();
  await expect(second.getByRole('textbox',{name:'完整视频目录',exact:true})).toHaveValue(videoDir);
  const save=second.getByRole('button',{name:'保存设置',exact:true}),dialog=second.getByRole('dialog');
  await save.click();
  await expect(dialog).toContainText('不限制视频空间');
  await first.getByRole('tab',{name:'存储',exact:true}).click();
  await first.getByRole('radio',{name:'不保留',exact:true}).check();
  await first.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(async()=>(await status()).settings.videoStorageMode).toBe('none');
  const rejected=second.waitForResponse(response=>new URL(response.url()).pathname==='/api/settings');
  await dialog.getByRole('button',{name:'确认并保存',exact:true}).click();
  expect((await rejected).status()).toBe(400);
  await expect(second.getByText('目录或视频保留策略已变化，请重新确认后保存。',{exact:true})).toBeVisible();
  expect((await status()).settings.videoDir).toBe(initial.settings.videoDir);
  await expect(second.getByRole('radio',{name:'不保留',exact:true})).toBeChecked();
  await save.click();
  await expect(dialog).toContainText('不保留普通视频');
  // A concurrent unrelated edit must not invalidate this new confirmation.
  await first.getByRole('tab',{name:'常规',exact:true}).click();
  await first.getByLabel('程序日志级别',{exact:true}).selectOption('debug');
  await first.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(async()=>(await status()).settings.logLevel).toBe('debug');
  await dialog.getByRole('button',{name:'确认并保存',exact:true}).click();
  await expect.poll(async()=>(await status()).settings.videoDir).toBe(videoDir);
  const confirmed=await status();
  expect(confirmed.settings.videoStorageMode).toBe('none');
  expect(confirmed.settings.logLevel).toBe('debug');
  expect(errors).toEqual([]);
  await writeFile(join(output,'settings-result.json'),JSON.stringify({passed:true,logLevel:saved.settings.logLevel,downloadConcurrencySoftLimit:saved.settings.downloadConcurrencySoftLimit,policyConfirmation:{staleRejected:true,reconfirmed:true,unrelatedLogLevel:confirmed.settings.logLevel},errors},null,2));
  console.log('Separate browser saves preserved edits and rejected a stale directory policy confirmation.');
} finally {await browser.close();}
