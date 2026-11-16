import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

const title='建议先测试下载速度';
const storageKey='swd-speed-test-suggestion';

for(const width of [390,1280])test(`first-use speed suggestion waits for consent and only navigates (${width})`,async({page},info)=>{
  await page.setViewportSize({width,height:900});
  await page.emulateMedia({colorScheme:width===390?'dark':'light'});
  const {terms,calls}=await mockAPI(page,{firstUse:true});terms.accepted=false;
  await page.goto('/?gate=1');
  const notice=page.getByRole('listitem',{name:title,exact:true});
  await expect(page.getByRole('alertdialog')).toBeVisible();
  await expect(notice).toHaveCount(0);
  expect(await page.evaluate(key=>localStorage.getItem(key),storageKey)).toBeNull();
  await page.getByRole('button',{name:'同意完整条款',exact:true}).click();
  await expect(notice).toBeVisible();
  const box=await notice.boundingBox();
  expect(box!.y).toBeGreaterThan(450);
  if(width===1280)expect(box!.x).toBeGreaterThan(640);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('speed-test-suggestion.png'),animations:'disabled'});
  await notice.getByRole('link',{name:'前往测速',exact:true}).focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#network-tests')).toBeFocused();
  await expect(page.getByRole('heading',{name:'下载速度测试',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'开始下载速度测试',exact:true})).toBeEnabled();
  await expect(notice).toHaveCount(0);
  expect(calls.some(call=>call.path==='settings'||call.path==='network/calibration/start')).toBe(false);
});

test('the browser remembers the suggestion across reloads and tabs, independently of application settings',async({page,browser})=>{
  await mockAPI(page,{firstUse:true});await page.goto('/');
  const notice=page.getByRole('listitem',{name:title,exact:true});
  await expect(notice).toBeVisible();
  await notice.getByRole('button',{name:'关闭通知',exact:true}).click();
  await navigate(page,'曲库');await navigate(page,'加速');await page.reload();
  await expect(page.getByRole('heading',{name:'加速',level:1,exact:true})).toBeVisible();
  await expect(notice).toHaveCount(0);
  const later=await page.context().newPage();
  try {
    const {state}=await mockAPI(later,{firstUse:true});
    state.settings.databasePath='';state.activeSettings.databasePath='';
    await later.goto('/');
    await expect(later.getByRole('heading',{name:'加速',level:1,exact:true})).toBeVisible();
    await expect(later.getByRole('listitem',{name:title,exact:true})).toHaveCount(0);
  } finally {await later.close();}
  const fresh=await browser.newContext();
  try {
    const first=await fresh.newPage();await mockAPI(first,{firstUse:true});
    await first.goto('http://127.0.0.1:18473/');
    await expect(first.getByRole('listitem',{name:title,exact:true})).toBeVisible();
  } finally {await fresh.close();}
});

for(const condition of ['unavailable','recovery'] as const)test(`a ${condition} startup preserves the first-use suggestion for a normal page`,async({page})=>{
  await mockAPI(page,{firstUse:true});
  if(condition==='unavailable')await page.route('**/api/status',route=>route.fulfill({status:503,body:'暂时无法读取程序状态'}));
  await page.goto(condition==='recovery'?'/?recovery=1':'/');
  if(condition==='recovery')await expect(page.getByText('SWD 启动失败',{exact:true})).toBeVisible();
  else await expect(page.getByRole('button',{name:'后台程序：读取失败，查看详情',exact:true})).toBeVisible();
  await expect(page.getByRole('listitem',{name:title,exact:true})).toHaveCount(0);
  expect(await page.evaluate(key=>localStorage.getItem(key),storageKey)).toBeNull();
  if(condition==='unavailable')await page.unroute('**/api/status');
  await page.goto('/');
  await expect(page.getByRole('listitem',{name:title,exact:true})).toBeVisible();
});

for(const failure of ['read','write'] as const)test(`browser storage ${failure} failure keeps the suggestion and network navigation usable`,async({page})=>{
  await mockAPI(page,{firstUse:true});
  const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));
  await page.addInitScript(({failure,key})=>{
    if(failure==='read') {
      const read=Storage.prototype.getItem;
      Storage.prototype.getItem=function(name){if(this===localStorage&&name===key)throw new DOMException('Storage unavailable','SecurityError');return read.call(this,name);};
    } else {
      const write=Storage.prototype.setItem;
      Storage.prototype.setItem=function(name,value){if(this===localStorage&&name===key)throw new DOMException('Storage full','QuotaExceededError');return write.call(this,name,value);};
    }
  },{failure,key:storageKey});
  await page.goto('/');
  await page.getByRole('listitem',{name:title,exact:true}).getByRole('link',{name:'前往测速',exact:true}).click();
  await expect(page.locator('#network-tests')).toBeFocused();
  await expect(page.getByRole('listitem',{name:title,exact:true})).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole('listitem',{name:title,exact:true})).toBeVisible();
  expect(errors).toEqual([]);
});
