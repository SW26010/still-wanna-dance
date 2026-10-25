import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

for(const path of ['/?gate=1','/terms?gate=1'])test(`unaccepted terms open automatically and gate business reads (${path})`,async({page})=>{
  const api=await mockAPI(page);api.terms.accepted=false;
  await page.goto(path);
  const dialog=page.getByRole('alertdialog');
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('article')).toContainText(api.terms.text);
  await page.keyboard.press('Escape');await expect(dialog).toBeVisible();
  expect(api.calls.some(c=>c.path==='status'||c.path==='terms/accept')).toBe(false);
  await dialog.getByRole('button',{name:'同意完整条款'}).click();
  await expect(dialog).toHaveCount(0);
  await expect.poll(()=>api.calls.filter(c=>c.path==='status').length).toBeGreaterThan(0);
  expect(api.calls.filter(c=>c.path==='terms/accept').map(c=>c.body)).toEqual([{version:'fixture',sha256:'fixture-hash',agree:true,contentRights:true}]);
  expect(api.calls.some(c=>c.path.startsWith('setup'))).toBe(false);
  await page.reload();await expect(dialog).toHaveCount(0);
});

test('failed consent remains modal and retries without enabling business reads',async({page})=>{
  const api=await mockAPI(page);api.terms.accepted=false;api.terms.needsConfirmation=true;let fail=true;
  await page.route('**/api/terms/accept',async r=>{
    if(fail)return r.fulfill({status:500,json:{error:'凭据无法保存'}});
    api.terms.accepted=true;await r.fulfill({json:{ok:true}});
  });
  await page.goto('/?gate=1');const dialog=page.getByRole('alertdialog');
  await expect(dialog).toContainText('之前的同意记录无法验证');
  await dialog.getByRole('button',{name:'同意完整条款'}).click();
  await expect(dialog).toContainText('凭据无法保存');
  expect(api.calls.some(c=>c.path==='status')).toBe(false);
  fail=false;await dialog.getByRole('button',{name:'同意完整条款'}).click();
  await expect(dialog).toHaveCount(0);await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeVisible();
});

test('unavailable terms can be retried and declining exits without accepting',async({page})=>{
  const api=await mockAPI(page);api.terms.accepted=false;let fail=true;
  await page.route('**/api/terms/current',r=>fail?r.fulfill({status:503,body:'暂不可读'}):r.fulfill({json:api.terms}));
  await page.goto('/?gate=1');const dialog=page.getByRole('alertdialog');
  await expect(dialog.getByRole('button',{name:'同意完整条款'})).toBeDisabled();
  await expect(dialog).toContainText('暂不可读');fail=false;
  await dialog.getByRole('button',{name:'重新读取条款'}).click();
  await expect(dialog.getByRole('button',{name:'同意完整条款'})).toBeEnabled();
  await dialog.getByRole('button',{name:'不同意并退出'}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='exit').length).toBe(1);
  expect(api.calls.some(c=>c.path==='terms/accept'||c.path==='status')).toBe(false);
});

test('terms fit a narrow viewport and retain keyboard focus',async({page},info)=>{
  const api=await mockAPI(page);api.terms.accepted=false;
  await page.setViewportSize({width:320,height:800});await page.emulateMedia({colorScheme:'dark'});
  await page.goto('/?gate=1');const dialog=page.getByRole('alertdialog');
  await expect(dialog).toBeVisible();
  for(let i=0;i<12;i++){await page.keyboard.press('Tab');expect(await dialog.evaluate(el=>el.contains(document.activeElement))).toBe(true);}
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('terms-mobile.png'),fullPage:true,animations:'disabled'});
});

test('old wizard links and receipts cannot restore removed work',async({page})=>{
  const api=await mockAPI(page);
  await page.addInitScript(()=>sessionStorage.setItem('swd-setup-application','old-receipt'));
  for(const path of ['/?setup=1','/#/setup']){
    await page.goto(path);await expect(page.getByRole('heading',{level:1,name:'加速',exact:true})).toBeVisible();
    await expect(page.getByRole('alertdialog')).toHaveCount(0);
  }
  expect(api.calls.some(c=>c.path.startsWith('setup'))).toBe(false);
});
