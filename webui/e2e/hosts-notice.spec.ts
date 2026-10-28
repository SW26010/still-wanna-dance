import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

const noticeTitle='可在退出后保留游戏接入';

for(const width of [320,1280])test(`authorized helper offers a focused exit setting without changing drafts (${width})`,async({page},info)=>{
  const {state,calls}=await mockAPI(page);
  await page.setViewportSize({width,height:850});
  await page.emulateMedia({colorScheme:width===320?'dark':'light'});
  await page.goto('/#prepareAheadSeconds');
  await page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true}).fill('777');
  await expect(page.getByText(noticeTitle,{exact:true})).toHaveCount(0);

  // A tray/automatic authorization reaches any page through the ordinary status snapshot.
  state.hosts.authorizationID='authorization-first';
  await expect(page.getByText(noticeTitle,{exact:true})).toBeVisible({timeout:10000});
  await expect(page.getByRole('link',{name:'打开退出设置',exact:true})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.getByRole('link',{name:'打开退出设置',exact:true}).click();
  await expect(page).toHaveURL(/#restoreHostsOnExit$/);
  await expect(page.getByRole('tab',{name:'常规',exact:true})).toHaveAttribute('aria-selected','true');
  const restore=page.getByRole('checkbox',{name:'停止加速或退出时恢复游戏直连',exact:true});
  await expect(restore).toBeFocused();
  await expect(restore).toBeInViewport();
  await expect(restore).toBeChecked();
  expect(calls.some(call=>call.path==='settings')).toBe(false);
  await page.screenshot({path:info.outputPath('exit-settings.png'),animations:'disabled'});
  await page.getByRole('tab',{name:'预加载与下载',exact:true}).click();
  await expect(page.getByRole('spinbutton',{name:'提前准备时间（秒）',exact:true})).toHaveValue('777');
});

test('authorization completed before opening the page is shown once, and later authorizations notify again',async({page})=>{
  const {state}=await mockAPI(page);state.hosts.authorizationID='authorization-first';
  await page.goto('/');
  await expect(page.getByText(noticeTitle,{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'关闭通知',exact:true}).click();
  await expect(page.getByText(noticeTitle,{exact:true})).toHaveCount(0);
  await page.waitForResponse(response=>new URL(response.url()).pathname==='/api/status');
  await expect(page.getByText(noticeTitle,{exact:true})).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeEnabled();
  await expect(page.getByText(noticeTitle,{exact:true})).toHaveCount(0);
  state.hosts.authorizationID='authorization-next';
  await expect(page.getByText(noticeTitle,{exact:true})).toBeVisible({timeout:10000});
});

test('canceled authorization has no settings suggestion; successful authorization stacks with the action result',async({page})=>{
  const {state}=await mockAPI(page);
  await page.route('**/api/activation/enable',route=>route.fulfill({status:400,json:{error:'管理员授权已取消'}}));
  await page.goto('/');
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByText('管理员授权已取消',{exact:true})).toBeVisible();
  await expect(page.getByText(noticeTitle,{exact:true})).toHaveCount(0);
  await page.unroute('**/api/activation/enable');
  await page.route('**/api/activation/enable',route=>{
    state.hosts.authorizationID='authorization-first';
    return route.fallback();
  });
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByText('视频加速已开启。',{exact:true})).toBeVisible();
  await expect(page.getByText(noticeTitle,{exact:true})).toBeVisible();
});

for(const failure of ['access','read','write'] as const)test(`notification storage failures preserve the WebUI and settings link (${failure})`,async({page})=>{
  const {state}=await mockAPI(page);state.hosts.authorizationID='authorization-first';
  const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));
  await page.addInitScript(failure=>{
    const fail=()=>{throw new DOMException('Injected notification storage failure',failure==='write'?'QuotaExceededError':'SecurityError');};
    if(failure==='access'){
      Object.defineProperty(window,'sessionStorage',{get:fail});
    }else{
      const storage=window.sessionStorage;
      if(failure==='read'){
        const read=Storage.prototype.getItem;
        Storage.prototype.getItem=function(key){if(this===storage&&key==='swd-hosts-authorization')fail();return read.call(this,key);};
      }else{
        const write=Storage.prototype.setItem;
        Storage.prototype.setItem=function(key,value){if(this===storage&&key==='swd-hosts-authorization')fail();return write.call(this,key,value);};
      }
    }
  },failure);
  await page.goto('/');
  await expect(page.getByRole('heading',{name:'加速',level:1,exact:true})).toBeVisible();
  await expect(page.getByText(noticeTitle,{exact:true})).toBeVisible();
  await page.getByRole('link',{name:'打开退出设置',exact:true}).click();
  await expect(page.getByRole('checkbox',{name:'停止加速或退出时恢复游戏直连',exact:true})).toBeFocused();
  await expect(page.getByText(noticeTitle,{exact:true})).toHaveCount(0);
  await page.waitForResponse(response=>new URL(response.url()).pathname==='/api/status');
  await expect(page.getByText(noticeTitle,{exact:true})).toHaveCount(0);
  state.hosts.authorizationID='authorization-next';
  await expect(page.getByText(noticeTitle,{exact:true})).toBeVisible({timeout:10000});
  await page.getByRole('button',{name:'关闭通知',exact:true}).click();
  await expect(page.getByText(noticeTitle,{exact:true})).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole('heading',{name:'设置',level:1,exact:true})).toBeVisible();
  await expect(page.getByText(noticeTitle,{exact:true})).toBeVisible();
  expect(errors).toEqual([]);
});
