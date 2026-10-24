import {navigate,navigateStep} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('settings anchors focus after initial reads and within the same page',async({page})=>{
  await mockAPI(page);await page.goto('/#prepareAheadSeconds');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间/})).toBeFocused();
  await page.evaluate(()=>{location.hash='maxCacheGiB';});
  await expect(page.locator('#maxCacheGiB')).toBeFocused();
  await page.evaluate(()=>{location.hash='recent';});await expect(page.locator('#recent')).toBeFocused();
});

test('acceleration owns connection actions and queue status links to settings',async({page})=>{
 const api=await mockAPI(page);
 api.state.queue.songs=[{songId:-1},{songId:1},{songId:-1},{songId:2},{songId:3},{songId:4}];
 await page.goto('/#network-access');
 await page.route('**/api/activation/enable',r=>r.fulfill({status:409,json:{error:'管理员取消，hosts 未改变'}}));
 await page.getByRole('button',{name:'修复并开启加速',exact:true}).click();
 await expect(page.getByText('管理员取消，hosts 未改变',{exact:true})).toBeVisible();
 await navigate(page,'视频加速');
 await expect(page.locator('#queue ol li')).toHaveCount(6);
 await expect(page.getByRole('button',{name:/开启队列预加载|关闭队列预加载/})).toHaveCount(0);
 await page.getByRole('link',{name:'调整提前准备时间',exact:true}).click();
 await expect(page.getByRole('spinbutton',{name:/提前准备时间/})).toBeFocused();
 expect(api.calls.some(c=>['start','stop','queue/start','queue/stop','hosts/enable'].includes(c.path))).toBe(false);
});

test('Mihomo preview stays a draft and clearing it releases only that preview',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#network-proxy');
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  await page.getByRole('textbox',{name:'粘贴节点内容',exact:true}).fill('fixture-node');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await page.route('**/api/proxy/import',r=>r.fulfill({json:{id:'preview-1',accepted:1,skipped:0,failed:0,items:[{index:1,status:'accepted',protocol:'ss'}]}}));
  await page.getByRole('button',{name:'检查并导入到草稿'}).click();
  await expect(page.getByText(/接受 1 个/)).toBeVisible();expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await page.getByRole('button',{name:'清除本次导入草稿'}).click();
  await expect.poll(()=>api.calls.find(c=>c.path==='proxy/import/discard')?.body).toEqual({id:'preview-1'});
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('external-socks5');
  await page.getByRole('textbox',{name:/SOCKS5 地址/}).fill('127.0.0.1:7891');
  await page.getByRole('textbox',{name:'SOCKS5 用户名（可选）',exact:true}).fill('fixture-user');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='settings').length).toBe(1);
  expect(api.calls.find(c=>c.path==='settings')?.body).not.toHaveProperty('socks5Password');
});

test('failed consent persistence keeps business APIs gated, retry succeeds',async({page})=>{
  const api=await mockAPI(page);api.terms.accepted=false;let fail=true;
  await page.route('**/api/terms/accept',async r=>{if(fail)return r.fulfill({status:500,json:{error:'凭据无法保存'}});api.terms.accepted=true;await r.fulfill({json:{ok:true}});});
  await page.goto('/?gate=1');await page.getByRole('button',{name:'阅读并同意条款',exact:true}).click();
  await page.getByRole('button',{name:'同意完整条款'}).click();
  await expect(page.getByRole('dialog').getByText(/凭据无法保存/)).toBeVisible();
  expect(api.calls.some(c=>c.path==='status')).toBe(false);fail=false;
  await page.getByRole('button',{name:'同意完整条款'}).click();await expect(page.getByRole('button',{name:'下一步',exact:true})).toBeEnabled();
  await page.getByRole('button',{name:'下一步',exact:true}).click();api.terms.accepted=false;api.terms.needsConfirmation=true;
  await navigateStep(page,'1. 开始');
  await expect(page.getByRole('dialog')).toBeVisible();await page.keyboard.press('Escape');
  await expect(page.getByRole('button',{name:'下一步',exact:true})).toBeDisabled();
});

test('setup validation failure keeps the draft and does not apply',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/setup');await navigateStep(page,'2. 存储');
  await page.getByRole('radio',{name:'限制为',exact:true}).check();await page.getByRole('spinbutton',{name:/视频存储预算/}).fill('18');
  await page.route('**/api/setup/validate',r=>r.fulfill({status:409,json:{error:'设置版本已变化，请重新进入引导'}}));
  await page.getByRole('button',{name:'下一步',exact:true}).click();await expect(page.getByText(/本页尚未通过检查/)).toBeVisible();
  await expect(page.getByRole('spinbutton',{name:/视频存储预算/})).toHaveValue('18');expect(api.calls.some(c=>c.path==='setup/apply')).toBe(false);
  await page.getByRole('button',{name:'放弃草稿并退出引导'}).click();await expect(page.getByRole('link',{name:'视频加速',exact:true})).toBeVisible();
});

test('reload reconciles the original application ID without submitting again',async({page})=>{
  const api=await mockAPI(page);api.state.setupApplication={id:'original-application',phase:'completed',configSaved:true};
  await page.addInitScript(()=>sessionStorage.setItem('swd-setup-application','original-application'));
  await page.goto('/#/setup');await expect(page.getByRole('link',{name:'视频加速',exact:true})).toBeVisible();
  expect(api.calls.find(c=>c.path==='setup/receipt')?.body).toEqual({id:'original-application'});
  expect(api.calls.some(c=>c.path==='setup/apply')).toBe(false);
  expect(await page.evaluate(()=>sessionStorage.getItem('swd-setup-application'))).toBeNull();
});

test('history clears failed reads and recovers focus after a row disappears',async({page})=>{
  await mockAPI(page);let failed=false;
  const rows=[2,1].map(id=>({id,at:1700000000000+id*1000,resource:'fixture-resource',method:'GET',range:'bytes=0-',elapsedMS:100,bytes:200,status:206,cache:'HIT',outcome:'completed',songs:[{id:'1',title:'分段歌曲'}]}));
  await page.route('**/api/history?*',r=>failed?r.fulfill({status:500,json:{error:'unavailable'}}):r.fulfill({json:{storageID:'fixture',requests:rows,hasMore:false}}));
  await page.clock.install();await page.goto('/#recent');
  const group=page.getByRole('button',{name:'请求 2 的明细',exact:true});await group.click();
  await expect(page.getByText('请求处理完成',{exact:true})).toHaveCount(2);
  failed=true;await page.clock.fastForward(5100);
  await expect(page.getByText('历史读取失败，请重试。')).toBeVisible();await expect(group).toHaveCount(0);await expect(page.getByLabel('历史查询结果',{exact:true})).toBeFocused();
});

test('late monitor response cannot overwrite paused display',async({page})=>{
  await mockAPI(page);let count=0,release:()=>void=()=>{};
  const held=new Promise<void>(done=>{release=done;});
  await page.route('**/api/downloads',async r=>{count++;if(count===2)await held;await r.fulfill({json:{tasks:[],bytesPerSecond:count===1?1000000:9000000}}).catch(()=>{});});
  await page.clock.install();await page.goto('/#/home');await page.getByRole('button',{name:'下载诊断',exact:true}).click();await expect(page.getByText(/总下载速度 1.000 MB/)).toBeVisible();
  await page.clock.fastForward(5100);await expect.poll(()=>count).toBe(2);
  await page.getByRole('checkbox',{name:'暂停下载展示'}).check();release();
  await page.clock.fastForward(5100);await expect(page.getByText(/总下载速度 1.000 MB/)).toBeVisible();expect(count).toBe(2);
  await page.getByRole('checkbox',{name:'暂停下载展示'}).uncheck();await expect(page.getByText(/总下载速度 9.000 MB/)).toBeVisible();
});

test('rejected cancellation stays running and stale SSE events cannot replace newer state',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/setup');await navigateStep(page,'3. 网络');
  await page.getByRole('button',{name:'开始连通性检测'}).click();
  await expect(page.getByRole('button',{name:'停止并放弃连通性检测'})).toBeEnabled();
  await page.route('**/api/setup/network/connectivity/cancel',r=>r.fulfill({status:503,json:{error:'取消未送达'}}));
  await page.getByRole('button',{name:'停止并放弃连通性检测'}).click();
  await expect(page.getByText(/操作未确认.*取消未送达/)).toBeVisible();
  await expect(page.getByRole('button',{name:'停止并放弃连通性检测'})).toBeEnabled();
  Object.assign(api.diagnostics.connectivity,{revision:1,run:0,running:false,phase:'idle'});await api.emit('connectivity');
  await expect(page.getByRole('button',{name:'停止并放弃连通性检测'})).toBeEnabled();
});

test('cache failures remove stale selectable rows and retry reads no unrelated services',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/cache');await expect(page.getByRole('checkbox',{name:/测试歌曲 1/})).toBeEnabled();
  await page.route('**/api/library?*',r=>r.fulfill({status:503,json:{error:'busy'}}));
  await page.getByRole('button',{name:'刷新曲库与本地占用'}).click();await expect(page.getByText(/曲库读取失败/)).toBeVisible();
  await expect(page.getByRole('checkbox',{name:/测试歌曲 1/})).toHaveCount(0);
  expect(api.calls.some(c=>c.path==='cache/delete'||c.path==='inventory/scan'||c.path==='start')).toBe(false);
});

test('320px forced-colors layout keeps every console page usable',async({page})=>{
  await mockAPI(page);await page.setViewportSize({width:320,height:800});await page.emulateMedia({forcedColors:'active',reducedMotion:'reduce'});await page.goto('/');
  for(const name of ['视频加速','时间线','曲库','设置','关于']){
    await navigate(page,name);
    await page.getByRole('button',{name:'展开页面导航',exact:true}).click();
    await expect(page.getByRole('navigation',{name:'页面导航',exact:true}).getByRole('link',{name,exact:true})).toHaveAttribute('aria-current','page');
    await page.keyboard.press('Escape');
    await expect(page.getByRole('button',{name:'展开页面导航',exact:true})).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  }
  await navigate(page,'帮助');await page.getByRole('link',{name:'重新进入使用引导'}).click();
  await navigateStep(page,'3. 网络');
  await expect(page.getByRole('button',{name:'开始下载速度测试'})).toBeEnabled();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
