import {navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('key consequences stay near their related choices',async({page})=>{
  await mockAPI(page);await page.goto('/#storage-settings');
  for (const name of ['常规','存储','下载与缓存','网络']) {
    await page.getByRole('tab',{name,exact:true}).click();
    await expect(page.getByText('运行配置保存后重启生效。',{exact:true})).toHaveCount(0);
    await expect(page.getByText('保存后立即用于停止加速和退出。关闭后需开启加速才能播放。',{exact:true})).toHaveCount(0);
  }
  await page.evaluate(()=>{location.hash='video-import';});await expect(page.getByText(/请勿修改源视频/)).toBeVisible();
  await page.evaluate(()=>{location.hash='network-proxy';});
  const upstream = page.getByRole('combobox',{name:'上游连接',exact:true});
  await expect(upstream.locator('option')).toHaveText(['直连','代理']);
  await upstream.selectOption('proxy');
  await expect(page.getByText('留空保留已有密码；清空用户名可移除认证。',{exact:true})).toBeVisible();
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  await navigate(page,'加速');await page.getByRole('status',{name:'游戏连接',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('可能请求 Windows 管理员授权');
});

test('license attribution and explicit consent remain available',async({page})=>{
  await mockAPI(page);await page.goto('/about');
  await expect(page.getByText('把视频准备交给 Still Wanna Dance，把时间留给跳舞。',{exact:true})).toBeVisible();
  await page.getByRole('tab',{name:'许可证与声明',exact:true}).click();
  const licenses=page.getByRole('tabpanel',{name:'许可证与声明',exact:true});
  for(const text of ['GNU GPLv3','GPL-3.0-only','历史 MIT 授权仍有效','软件许可不授予视频、音乐等内容的权利'])await expect(licenses).toContainText(text);
  await expect(licenses.getByRole('link',{name:'软件许可证原文',exact:true})).toHaveAttribute('href','/assets/webui/software-license.txt');
  await expect(licenses.getByRole('link',{name:'程序第三方声明原文',exact:true})).toHaveAttribute('href','/assets/webui/third-party-notices.txt');
  await navigate(page,'使用条款');
  await expect(page.getByRole('article',{name:'完整使用条款'})).toContainText('浏览器测试条款');
});
