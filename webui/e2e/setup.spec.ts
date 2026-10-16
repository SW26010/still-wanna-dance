import {navigate,navigateStep} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('first entry gates business reads, persists explicit consent, and applies one reviewed plan',async({page})=>{
  const api=await mockAPI(page);api.terms.accepted=false;
  await page.goto('/?gate=1#/settings');
  await expect(page.getByRole('button',{name:'下一步',exact:true})).toBeDisabled();
  expect(api.calls.some(c=>c.path==='status')).toBe(false);
  await page.getByRole('button',{name:'完整条款',exact:true}).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.getByRole('button',{name:'同意完整条款'}).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await expect(page.getByRole('heading',{name:'存储',exact:true})).toBeVisible();
  await page.getByRole('checkbox',{name:'指定其他位置（程序文件夹外）'}).check();
  await page.getByRole('button',{name:'C:\\UI-Test\\storage',exact:true}).click();
  await expect(page.getByRole('button',{name:'D:\\UI-Test\\cache',exact:true})).toBeVisible();
  await page.getByRole('checkbox',{name:'导入已有视频',exact:true}).check();
  await page.getByRole('button',{name:'点击选择文件夹'}).click();
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await expect(page.getByRole('heading',{name:'网络',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeEnabled();
  expect(api.calls.some(c=>c.path.endsWith('/start')&&c.path.startsWith('setup/network'))).toBe(false);
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await expect(page.getByRole('heading',{name:'应用配置',exact:true})).toBeVisible();
  expect(api.calls.some(c=>c.path==='setup/apply'||c.path==='hosts/enable')).toBe(false);
  await page.getByRole('button',{name:'确认并应用',exact:true}).click();
  await expect(page.getByRole('link',{name:'开始使用',exact:true})).toBeVisible();
  const apply=api.calls.filter(c=>c.path==='setup/apply');expect(apply).toHaveLength(1);
  expect(apply[0].body).toMatchObject({baseVersion:'1',storageDir:'D:\\UI-Test\\cache',importSource:'D:\\UI-Test\\videos',customStorage:true});
  expect(apply[0].body.id).toMatch(/^[0-9a-f-]{36}$/);
  expect(api.calls.filter(c=>c.path==='terms/accept')[0].body).toEqual({version:'fixture',sha256:'fixture-hash',agree:true,contentRights:true});
});
test('wizard draft is independent of settings and failed apply never resubmits automatically',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/settings');
  await page.getByRole('spinbutton',{name:/提前准备时间 M/}).fill('40');
  await page.getByRole('link',{name:'重新打开使用引导'}).click();
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await page.getByRole('radio',{name:'限制为',exact:true}).check();
  await page.getByRole('spinbutton',{name:/缓存容量/}).fill('20');
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  let applyCount=0;
  await page.route('**/api/setup/apply',async r=>{applyCount++;const body=r.request().postDataJSON();api.state.setupApplication={id:body.id,phase:'uncertain'};await r.abort();});
  await page.getByRole('button',{name:'确认并应用',exact:true}).click();
  await expect(page.getByRole('button',{name:'核对应用结果'})).toBeVisible();
  await page.getByRole('button',{name:'核对应用结果'}).click();expect(applyCount).toBe(1);
  await page.getByRole('button',{name:'放弃更改并退出引导'}).click();
  await navigate(page,'设置');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间 M/})).toHaveValue('40');
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
});
test('manual SSE diagnostics restore saved results when cancellation is confirmed but response is lost',async({page})=>{
  const api=await mockAPI(page);
  Object.assign(api.diagnostics.calibration,{lastValid:{direct:{singleSpeedBPS:500000,fullSpeedBPS:2000000,fullConcurrency:8,fullCPUPercent:3,cpuP99Percent:5,referenceBitrateBPS:2000000}}});
  await api.emit('calibration');
  await page.goto('/#/setup');await navigateStep(page,'3. 网络');
  await expect(page.getByRole('button',{name:'开始CF 并发测速'})).toBeEnabled();
  await expect(page.getByText('16.00 Mbps',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'开始CF 并发测速'}).click();
  await expect(page.getByRole('button',{name:'停止并放弃CF 并发测速'})).toBeEnabled();
  await page.route('**/api/setup/network/calibration/cancel',async r=>{Object.assign(api.diagnostics.calibration,{revision:10,running:false,phase:'canceled'});await api.emit('calibration');await r.abort();});
  await page.getByRole('button',{name:'停止并放弃CF 并发测速'}).click();
  await expect(page.getByText('16.00 Mbps',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'开始CF 并发测速'})).toBeEnabled();
  expect(api.calls.filter(c=>c.path==='setup/network/calibration/start')).toHaveLength(1);
});
test('wizard mobile layout and terms focus trap',async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width:390,height:844});await page.goto('/#/setup');
  for(const label of ['1. 开始','2. 存储','3. 网络','4. 启用']){
    await navigateStep(page,label);
    await expect(page.getByRole('heading',{name:label.slice(3),level:1,exact:true})).toBeFocused();
    await expect(page.getByRole('button',{name:label==='4. 启用'?'确认并应用':'下一步',exact:true})).toBeEnabled();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:info.outputPath(label+'.png'),fullPage:true,animations:'disabled'});
  }
  await navigateStep(page,'1. 开始');await page.getByRole('button',{name:'完整条款',exact:true}).click();
  for(let i=0;i<8;i++){await page.keyboard.press('Tab');expect(await page.getByRole('dialog').evaluate(el=>el.contains(document.activeElement))).toBe(true);}
  await page.keyboard.press('Escape');await expect(page.getByRole('dialog')).toHaveCount(0);
});
