import {test,expect,type Page,type Locator} from '@playwright/test';
import {mockAPI} from './api';

const labels=['后台程序','游戏连接','视频服务','房间队列'];
const badge=(page:Page,label:string)=>page.getByRole('status',{name:label,exact:true});
const background=(element:Locator)=>element.evaluate(el=>getComputedStyle(el).backgroundColor);
const healthy=async(page:Page)=>{
  for(const [label,state] of [['后台程序','已连接'],['游戏连接','已接入'],['视频服务','运行中'],['房间队列','监控中']])await expect(badge(page,label)).toHaveText(label+'：'+state);
};

for(const [width,theme,forced] of [[1280,'light',false],[390,'dark',false],[320,'light',true]] as const)test(`runtime states share Fluent status styling at ${width}px ${theme}`,async({page},info)=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;api.state.queue={running:true,desired:true};
  await page.setViewportSize({width,height:950});await page.emulateMedia({colorScheme:theme,forcedColors:forced?'active':'none'});await page.goto('/#/home');await healthy(page);
  await expect(page.locator('.condition-strip [role="status"]')).toHaveText(['游戏连接：已接入','视频服务：运行中','房间队列：监控中']);
  const styles=[];
  for(const label of labels){
    const item=badge(page,label);await expect(item).toHaveAttribute('aria-atomic','true');await expect(item.locator('svg')).toHaveAttribute('aria-hidden','true');
    styles.push(await item.evaluate(el=>{const s=getComputedStyle(el);return {background:s.backgroundColor,color:s.color,font:s.fontSize,height:el.getBoundingClientRect().height};}));
  }
  expect(new Set(styles.map(style=>JSON.stringify(style))).size).toBe(1);expect(styles[0].background).not.toBe(styles[0].color);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('runtime-states.png'),fullPage:true,animations:'disabled'});
});

test('failed polling invalidates running badges and recovery restores them',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;api.state.queue={running:true,desired:true};
  await page.clock.install();await page.goto('/#/home');await healthy(page);
  const green=await background(badge(page,'后台程序'));
  await page.route('**/api/status',route=>route.fulfill({status:503,json:{error:'offline'}}));await page.clock.fastForward(5100);
  await expect(badge(page,'后台程序')).toHaveText('后台程序：更新失败');
  for(const label of labels.slice(1))await expect(badge(page,label)).toHaveText(label+'：待确认');
  expect(await background(badge(page,'游戏连接'))).not.toBe(green);expect(await background(badge(page,'后台程序'))).not.toBe(green);
  await page.unroute('**/api/status');await page.clock.fastForward(5100);await healthy(page);
  await page.getByRole('button',{name:'退出程序',exact:true}).click();
  await expect(badge(page,'后台程序')).toHaveText('后台程序：未连接');
  for(const label of labels.slice(1))await expect(badge(page,label)).toHaveText(label+'：待确认');
});

test('conflicts, inactive services and startup have distinct semantic colors',async({page},info)=>{
  const api=await mockAPI(page);api.state.hosts.conflict=true;api.state.queue={desired:true,running:false};
  await page.clock.install();await page.goto('/#/home');
  await expect(badge(page,'后台程序')).toHaveText('后台程序：已连接');await expect(badge(page,'游戏连接')).toHaveText('游戏连接：接入冲突');
  await expect(badge(page,'视频服务')).toHaveText('视频服务：未运行');
  await expect(badge(page,'房间队列')).toHaveText('房间队列：未就绪');
  const colors=await Promise.all(labels.map(label=>background(badge(page,label))));expect(new Set(colors).size).toBe(4);
  await page.screenshot({path:info.outputPath('mixed-states.png'),fullPage:true,animations:'disabled'});
  api.state.activation={phase:'starting'};await page.clock.fastForward(5100);await expect(badge(page,'视频服务')).toHaveText('视频服务：启动中');
  expect(colors).not.toContain(await background(badge(page,'视频服务')));
  api.state.activation={};api.state.running=false;api.state.cdnError='视频服务启动失败';
  await page.clock.fastForward(5100);await expect(badge(page,'视频服务')).toHaveText('视频服务：异常');
  expect(await background(badge(page,'视频服务'))).toBe(colors[1]);
  api.state.actionErrors={queue:'无法启动队列'};await page.clock.fastForward(5100);
  await expect(badge(page,'房间队列')).toHaveText('房间队列：启动失败');expect(await background(badge(page,'房间队列'))).toBe(colors[1]);
  api.state.actionErrors={};api.state.queue={desired:true,running:true,logError:'无法读取游戏日志'};await page.clock.fastForward(5100);
  await expect(badge(page,'房间队列')).toHaveText('房间队列：需检查');expect(await background(badge(page,'房间队列'))).toBe(colors[3]);
  api.state.queue={desired:false,running:false};await page.clock.fastForward(5100);
  await expect(badge(page,'房间队列')).toHaveText('房间队列：已关闭');expect(await background(badge(page,'房间队列'))).toBe(colors[2]);
});

test('a rejected action does not misreport the backend connection',async({page})=>{
  await mockAPI(page);await page.route('**/api/exit',route=>route.fulfill({status:400,json:{error:'退出操作失败'}}));await page.goto('/#/home');
  await expect(badge(page,'后台程序')).toHaveText('后台程序：已连接');const green=await background(badge(page,'后台程序'));
  await page.getByRole('button',{name:'退出程序',exact:true}).click();
  await expect(page.getByRole('listitem',{name:'退出操作失败',exact:true})).toBeVisible();await expect(badge(page,'后台程序')).toHaveText('后台程序：已连接');
  expect(await background(badge(page,'后台程序'))).toBe(green);
});
