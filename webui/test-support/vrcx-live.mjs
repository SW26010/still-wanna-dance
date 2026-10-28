// Opt-in integration against an isolated real Console; never uses personal data.
import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const [baseURL, source, output, mode='first-run'] = process.argv.slice(2);
await mkdir(output, {recursive:true});
const browser = await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page = await browser.newPage({viewport:{width:1280,height:950}});
  const errors = [];
  page.on('pageerror',error=>errors.push(error.message));
  const calls=[];
  page.on('request',request=>{if(request.url().includes('/api/vrcx/'))calls.push(new URL(request.url()).pathname);});
  await page.goto(baseURL+'/');
  const notice=page.getByRole('listitem',{name:'检测到 VRCX 数据库',exact:true});
  if(mode==='first-run') {
    await expect(notice).toBeVisible();
    expect(calls).toEqual(['/api/vrcx/detect']);
    await page.screenshot({path:join(output,'toast.png'),animations:'disabled'});
    await notice.getByRole('link',{name:'前往曲库',exact:true}).click();
  } else {
    await expect.poll(()=>calls.length).toBe(1);
    await expect(notice).toHaveCount(0);
    await page.getByRole('navigation',{name:'页面导航',exact:true}).getByRole('link',{name:'曲库',exact:true}).click();
  }
  await expect(notice).toHaveCount(0);
  await expect(page.getByRole('button',{name:'导入历史',exact:true})).toBeVisible();
  expect(calls).toEqual(['/api/vrcx/detect']);
  await page.screenshot({path:join(output,'suggestion.png'),animations:'disabled'});
  await page.getByRole('button',{name:'导入历史',exact:true}).click();
  await expect(page.getByRole('listitem',{name:'已根据 VRCX 历史初始化 2 首歌曲的优先级。',exact:true})).toBeVisible();
  expect(calls).toEqual(['/api/vrcx/detect','/api/vrcx/preview','/api/vrcx/apply']);
  await page.reload();
  await expect(page.getByRole('button',{name:'导入 VRCX 历史',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'导入历史',exact:true})).toHaveCount(0);
  await page.getByRole('button',{name:'导入 VRCX 历史',exact:true}).click();
  await page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true}).check();
  await page.getByRole('textbox',{name:'VRCX 数据库路径',exact:true}).click();
  await expect(page.getByRole('textbox',{name:'VRCX 数据库路径',exact:true})).toHaveValue(source);
  await page.getByRole('button',{name:'读取并预览',exact:true}).click();
  await expect(page.getByText(/已有 2 首歌曲的需求统计/)).toBeVisible();
  const action=page.getByRole('button',{name:'导入并覆盖…',exact:true});
  await action.click();
  await expect(page.getByRole('dialog')).toContainText('整体被替换');
  await page.screenshot({path:join(output,'confirm.png'),animations:'disabled'});
  await page.getByRole('button',{name:'取消',exact:true}).click();
  await expect(action).toBeFocused();
  await action.click();
  await page.getByRole('button',{name:'强制导入并覆盖',exact:true}).click();
  await expect(page.getByText('已导入 2 次历史需求、2 首歌曲，跳过 2 条记录。')).toBeVisible();
  expect(errors).toEqual([]);
  await writeFile(join(output,'result.json'),JSON.stringify({passed:true,mode,importedEvents:2,songs:2,skipped:2,errors},null,2));
  console.log(`Real Console ${mode} detection, explicit quick import, reload and replacement confirmation passed.`);
} finally {
  await browser.close();
}
