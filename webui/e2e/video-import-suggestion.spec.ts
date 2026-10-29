import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

const suggestion={name:'已有本地视频？',exact:true};

for(const width of [1280,390])test(`first-run import suggestion waits for consent and opens the existing import panel at ${width}px`,async({page})=>{
  await page.setViewportSize({width,height:900});
  await page.emulateMedia({colorScheme:width===390?'dark':'light'});
  const {state,calls,terms}=await mockAPI(page);
  state.videoImportSuggested=true;state.hosts={ready:true,managed:false,message:'已接入'};terms.accepted=false;
  await page.goto('/?gate=1');
  const notice=page.getByRole('listitem',suggestion);
  await expect(page.getByRole('alertdialog')).toBeVisible();
  await expect(notice).toHaveCount(0);
  await page.getByRole('button',{name:'同意完整条款',exact:true}).click();
  await expect(notice).toBeVisible();
  await expect(notice).toContainText('原视频会保留，同一分区无需重复存储。');
  await expect(page.locator('#main').getByRole('listitem',suggestion)).toHaveCount(0);
  const box=await notice.boundingBox();
  expect(box!.y).toBeGreaterThan(450);
  expect(box!.x+box!.width).toBeLessThanOrEqual(width);
  if(width===1280)expect(box!.x).toBeGreaterThan(640);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:`../artifacts/video-import-toast/notice-${width}.png`,animations:'disabled'});
  await notice.getByRole('link',{name:'前往导入',exact:true}).focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#video-import')).toBeFocused();
  await expect(page.getByRole('textbox',{name:'外部目录',exact:true})).toHaveValue('');
  await expect(notice).toHaveCount(0);
  expect(calls.filter(call=>call.path.startsWith('import/')).map(call=>call.path)).toEqual(['import/dismiss-suggestion']);
  expect(calls.some(call=>call.path==='path/select'||call.path.startsWith('activation/'))).toBe(false);
  await page.reload();
  await expect(page.locator('#video-import')).toBeVisible();
  await expect(notice).toHaveCount(0);
});

for(const label of ['不再提示','关闭通知'])test(`${label} stays hidden through navigation and reload without changing settings`,async({page})=>{
  const {state,calls}=await mockAPI(page);state.videoImportSuggested=true;
  await page.goto('/');
  await page.getByRole('listitem',suggestion).getByRole('button',{name:label,exact:true}).click();
  await expect(page.getByRole('listitem',suggestion)).toHaveCount(0);
  await expect(page.locator('#main')).toBeFocused();
  await navigate(page,'曲库');await navigate(page,'加速');await page.reload();
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeVisible();
  await expect(page.getByRole('listitem',suggestion)).toHaveCount(0);
  expect(calls.filter(call=>call.path==='settings'||call.path.startsWith('import/')).map(call=>call.path)).toEqual(['import/dismiss-suggestion']);
});

test('an ineligible startup does not show the import suggestion',async({page})=>{
  await mockAPI(page);await page.goto('/');
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeVisible();
  await expect(page.getByRole('listitem',suggestion)).toHaveCount(0);
});

test('the suggestion can focus an import panel that is already open',async({page})=>{
  const {state}=await mockAPI(page);state.videoImportSuggested=true;
  await page.goto('/#video-import');
  await page.getByRole('listitem',suggestion).getByRole('link',{name:'前往导入',exact:true}).click();
  await expect(page.locator('#video-import')).toBeFocused();
  await expect(page.getByRole('listitem',suggestion)).toHaveCount(0);
});

test('failed acknowledgement retains the suggestion and permits retry',async({page})=>{
  const {state}=await mockAPI(page);state.videoImportSuggested=true;
  await page.route('**/api/import/dismiss-suggestion',route=>route.abort(),{times:1});
  await page.goto('/');
  const notice=page.getByRole('listitem',suggestion);
  await notice.getByRole('button',{name:'不再提示',exact:true}).click();
  await expect(notice).toContainText('未能关闭提示，请重试。');
  await notice.getByRole('button',{name:'不再提示',exact:true}).click();
  await expect(notice).toHaveCount(0);
});
