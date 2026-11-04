import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

const title='发现新版本 v1.0.0';
const releaseURL='https://github.com/SW26010/still-wanna-dance/releases/tag/v1.0.0';

test('startup notice opens About once and the version links to the release',async({page})=>{
  const {state,calls}=await mockAPI(page);
  state.update={checking:false,result:'available',checkedAt:'2026-11-04T01:00:00Z',latestVersion:'v1.0.0',releaseURL,startupNotice:true};
  await page.goto('/');
  await expect(page.getByRole('listitem',{name:title,exact:true})).toBeVisible();
  await page.getByRole('link',{name:'查看更新',exact:true}).click();
  await expect(page).toHaveURL(/#\/about$/);
  await expect(page.getByRole('link',{name:'v1.0.0',exact:true})).toHaveAttribute('href',releaseURL);
  await expect(page.getByRole('button',{name:'检查更新',exact:true})).toBeEnabled();
  await page.reload();
  await expect(page.getByRole('link',{name:'v1.0.0',exact:true})).toBeVisible();
  await expect(page.getByRole('listitem',{name:title,exact:true})).toHaveCount(0);
  expect(calls.filter(call=>call.path==='update/notice')).toHaveLength(1);
});

test('manual checks show progress and results without a notification or blocking navigation',async({page})=>{
  const {state}=await mockAPI(page);
  state.update={checking:false,result:'unknown',reason:'not_found',checkedAt:'2026-11-04T01:00:00Z'};
  let finish!:()=>void;
  const held=new Promise<void>(resolve=>{finish=resolve;});
  await page.route('**/api/update/check',async route=>{
    await held;
    state.update={checking:false,result:'current',checkedAt:'2026-11-04T02:00:00Z'};
    await route.fulfill({json:state.update});
  });
  await page.goto('/#/about');
  await expect(page.getByText('未找到正式版，检查结果未知',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'检查更新',exact:true}).click();
  await expect(page.getByText('正在检查更新…',{exact:true})).toBeVisible();
  await page.getByRole('link',{name:'网络',exact:true}).click();
  await expect(page.getByRole('heading',{name:'网络',level:1,exact:true})).toBeVisible();
  finish();
  await page.getByRole('link',{name:'关于',exact:true}).click();
  await expect(page.getByText(/暂无更新 · 上次检查：/)).toBeVisible();
  await expect(page.getByRole('listitem',{name:title,exact:true})).toHaveCount(0);
});

test('a failed manual check replaces the old result with unknown and can be retried',async({page})=>{
  const {state}=await mockAPI(page);
  state.update={checking:false,result:'current',checkedAt:'2026-11-04T01:00:00Z'};
  await page.route('**/api/update/check',route=>{
    state.update={checking:false,result:'unknown',reason:'failed',checkedAt:'2026-11-04T02:00:00Z'};
    return route.fulfill({json:state.update});
  });
  await page.goto('/#/about');
  await expect(page.getByText(/暂无更新 · 上次检查：/)).toBeVisible();
  await page.getByRole('button',{name:'检查更新',exact:true}).click();
  await expect(page.getByText('检查失败，结果未知',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'检查更新',exact:true})).toBeEnabled();
  await expect(page.getByRole('listitem',{name:title,exact:true})).toHaveCount(0);
});

test('a lost manual response does not relabel the old result as a successful check',async({page})=>{
  const {state}=await mockAPI(page);
  state.update={checking:false,result:'current',checkedAt:'2026-11-04T01:00:00Z'};
  await page.route('**/api/update/check',route=>route.abort('connectionrefused'));
  await page.goto('/#/about');
  await expect(page.getByText(/暂无更新 · 上次检查：/)).toBeVisible();
  const statusRefresh=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/status');
  await page.getByRole('button',{name:'检查更新',exact:true}).click();
  await statusRefresh;
  await expect(page.getByText('检查失败，结果未知',{exact:true})).toBeVisible();
  await expect(page.getByText(/暂无更新 · 上次检查：/)).toHaveCount(0);
});

test('the public About page explains that consent is needed to check',async({page})=>{
  const {calls}=await mockAPI(page);
  await page.goto('/about?gate');
  await expect(page.getByText('同意使用条款后可检查更新',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'检查更新',exact:true})).toBeDisabled();
  expect(calls.some(call=>call.path==='update/check')).toBe(false);
});
