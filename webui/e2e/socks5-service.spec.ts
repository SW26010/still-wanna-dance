import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {expectWithClock} from './clock';
import {navigate} from './navigation';

for(const theme of ['light','dark'] as const)test(`SOCKS5 service observations update independently in ${theme}`,async({page},info)=>{
  await page.clock.install();
  await page.setViewportSize({width:theme==='light'?390:320,height:844});
  const api=await mockAPI(page);
  api.state.settings.theme=api.state.activeSettings.theme=theme;
  // Saved direct settings must not hide the still-active SOCKS5 check.
  Object.assign(api.state.activeSettings,{upstreamMode:'proxy',proxyBackend:'external-socks5',socks5Address:'[::1]:1080'});
  api.state.settings.socks5Address='saved.invalid:9999';
  api.state.restartRequired=true;
  const at='2026-11-04T04:00:00Z',next='2026-11-04T04:00:30Z';
  api.state.socks5Service={checking:false,closed:false,state:'available',authenticated:true,latencyMS:12.4,observedAt:at,nextCheck:next};
  await page.goto('/#/network');
  const panel=page.locator('.panel').filter({has:page.getByRole('heading',{name:'SOCKS5 服务',exact:true})});
  await expect(panel).toContainText('服务：可用');
  await expect(panel.getByText('[::1]:1080',{exact:true})).toBeVisible();
  await expect(panel.getByText('saved.invalid:9999',{exact:true})).toHaveCount(0);
  await expect(panel.getByText('每 30 秒检查协商与认证，不连接外网目标。',{exact:true})).toHaveCount(0);
  await expect(panel.getByText('协商与认证通过',{exact:true})).toHaveCount(0);
  await expect(panel).toContainText('12 ms');
  await expect(panel.locator('time').first()).toHaveAttribute('datetime',at);
  await expect(panel.locator('time').last()).toHaveAttribute('datetime',next);
  await page.screenshot({path:info.outputPath(`socks5-service-${theme}.png`),fullPage:true,animations:'disabled'});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  api.state.socks5Service.checking=true;
  await expectWithClock(page,()=>panel.textContent()).toContain('服务：检测中');
  await expect(panel.getByText('12 ms',{exact:true})).toHaveCount(0);
  Object.assign(api.state.socks5Service,{checking:false,state:'failed',error:'SOCKS5 用户名或密码未获接受',latencyMS:null,observedAt:next});
  await expectWithClock(page,()=>panel.textContent()).toContain('服务：检查失败');
  await expect(panel.getByText('SOCKS5 用户名或密码未获接受',{exact:true})).toBeVisible();
  Object.assign(api.state.socks5Service,{state:'available',error:'',authenticated:false,latencyMS:8});
  await expectWithClock(page,()=>panel.textContent()).toContain('服务：可用');
  await navigate(page,'曲库');await navigate(page,'网络');
  await expect(panel).toContainText('服务：可用');
  expect(api.calls.filter(c=>c.path.startsWith('network/')&&c.path.endsWith('/start'))).toHaveLength(0);
  const refresh=panel.getByRole('button',{name:'重新检测 SOCKS5 服务',exact:true});
  let finish:()=>void=()=>{};
  const hold=new Promise<void>(resolve=>{finish=resolve;});
  await page.route('**/api/network/socks5/check',async route=>{await hold;await route.fulfill({json:{ok:true}});});
  await refresh.click();
  await expect(refresh).toBeDisabled();
  await expect(panel).toContainText('服务：检测中');
  finish();
  Object.assign(api.state.socks5Service,{observedAt:'2026-11-04T04:01:00Z'});
  await expectWithClock(page,()=>refresh.isEnabled()).toBe(true);
  await page.route('**/api/network/socks5/check',route=>route.fulfill({status:409,json:{error:'检测请求失败'}}));
  await refresh.click();
  await expect(page.getByRole('listitem',{name:/检测未确认：/})).toBeVisible();
  // Lost status reads must not leave a reassuring success badge.
  await page.route('**/api/status',route=>route.fulfill({status:503,body:'offline'}));
  await expectWithClock(page,()=>panel.textContent()).toContain('服务：待确认');
});

for(const mode of ['direct','mihomo'] as const)test(`SOCKS5 service is hidden for active ${mode}`,async({page})=>{
  const api=await mockAPI(page);
  Object.assign(api.state.settings,{upstreamMode:'proxy',proxyBackend:'external-socks5'});
  Object.assign(api.state.activeSettings,{upstreamMode:mode==='direct'?'direct':'proxy',proxyBackend:mode==='mihomo'?'mihomo':'external-socks5'});
  api.state.restartRequired=true;
  await page.goto('/#/network');
  await expect(page.getByRole('heading',{name:'连通性检测',exact:true})).toBeVisible();
  await expect(page.getByRole('heading',{name:'SOCKS5 服务',exact:true})).toHaveCount(0);
});
