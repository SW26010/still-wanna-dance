// Opt-in integration against an isolated real Console; never uses personal data.
import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const [baseURL, source, output] = process.argv.slice(2);
await mkdir(output, {recursive:true});
const browser = await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page = await browser.newPage({viewport:{width:1280,height:950}});
  const errors = [];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(baseURL+'/#/library');
  await page.getByRole('button',{name:'导入 VRCX 历史需求统计',exact:true}).click();
  await page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true}).check();
  await page.getByLabel('VRCX 数据库路径').fill(source);
  await page.getByRole('button',{name:'读取并预览',exact:true}).click();
  await expect(page.getByText('2 次 / 2 首',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'导入历史偏好',exact:true}).click();
  await expect(page.getByText('已导入 2 次历史需求、2 首歌曲，跳过 2 条记录。')).toBeVisible();
  await page.reload();
  await page.getByRole('button',{name:'导入 VRCX 历史需求统计',exact:true}).click();
  await page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true}).check();
  await page.getByLabel('VRCX 数据库路径').fill(source);
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
  await page.goto(baseURL+'/#/setup');
  await page.getByRole('navigation',{name:'引导步骤',exact:true}).getByRole('button',{name:'2. 存储',exact:true}).click();
  const option=page.getByRole('checkbox',{name:'从 VRCX 导入历史偏好',exact:true});
  await expect(option).toBeEnabled();await expect(option).not.toBeChecked();
  await expect(page.locator('.wizard').getByLabel('VRCX 数据库路径')).toHaveCount(0);
  await option.check();await expect(page.getByRole('dialog')).toHaveCount(0);
  await page.screenshot({path:join(output,'wizard-storage.png'),animations:'disabled',fullPage:true});
  await page.getByRole('navigation',{name:'引导步骤',exact:true}).getByRole('button',{name:'4. 启用',exact:true}).click();
  await expect(page.getByText(/VRCX 歌曲偏好：检查通过，2 次需求 \/ 2 首歌曲/)).toBeVisible();
  await page.screenshot({path:join(output,'wizard-check.png'),animations:'disabled',fullPage:true});
  await page.getByRole('button',{name:'确认并应用',exact:true}).click();
  await expect(page.getByRole('dialog')).toContainText('现有 2 首歌曲');
  await page.getByRole('button',{name:'取消',exact:true}).click();
  await page.getByRole('button',{name:'确认并应用',exact:true}).click();
  await page.getByRole('button',{name:'强制导入并覆盖',exact:true}).click();
  await expect(page.getByText(/配置已应用。.*VRCX 歌曲偏好：已导入 2 次历史需求、2 首歌曲/)).toBeVisible();
  expect(errors).toEqual([]);
  await writeFile(join(output,'result.json'),JSON.stringify({passed:true,importedEvents:2,songs:2,skipped:2,errors},null,2));
  console.log('Real Console settings import and deferred wizard check/confirm/replacement passed.');
} finally {
  await browser.close();
}
