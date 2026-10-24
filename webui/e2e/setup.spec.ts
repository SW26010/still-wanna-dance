import {navigate,navigateStep} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('first entry gates business reads, persists explicit consent, and applies one reviewed plan',async({page})=>{
  const api=await mockAPI(page);api.terms.accepted=false;
  await page.goto('/?gate=1#/settings');
  await expect(page.getByRole('button',{name:'下一步',exact:true})).toBeDisabled();
  expect(api.calls.some(c=>c.path==='status')).toBe(false);
  const helpOpened = page.waitForEvent('popup');
  await page.getByRole('link',{name:'帮助（新窗口）'}).click();
  const help = await helpOpened;
  await expect(help.getByRole('heading',{name:'帮助',exact:true})).toBeVisible();
  await help.close();
  expect(api.calls.some(c=>c.path==='terms/accept'||c.path==='status')).toBe(false);
  await page.getByRole('button',{name:'阅读并同意条款',exact:true}).click();
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
  await expect(page.getByRole('link',{name:'游戏加速',exact:true})).toBeVisible();
  const apply=api.calls.filter(c=>c.path==='setup/apply');expect(apply).toHaveLength(1);
  expect(apply[0].body).toMatchObject({baseVersion:'1',storageDir:'D:\\UI-Test\\cache',importSource:'D:\\UI-Test\\videos',customStorage:true});
  expect(apply[0].body.id).toMatch(/^[0-9a-f-]{36}$/);
  expect(api.calls.filter(c=>c.path==='terms/accept')[0].body).toEqual({version:'fixture',sha256:'fixture-hash',agree:true,contentRights:true});
});
test('wizard draft is independent of settings and failed apply never resubmits automatically',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#prepareAheadSeconds');
  await page.getByRole('spinbutton',{name:/提前准备时间/}).fill('140');
  await navigate(page,'帮助');await page.getByRole('link',{name:'重新进入使用引导',exact:true}).click();
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await page.getByRole('radio',{name:'限制为',exact:true}).check();
  await page.getByRole('spinbutton',{name:/视频存储预算/}).fill('20');
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  let applyCount=0;
  await page.route('**/api/setup/apply',async r=>{applyCount++;const body=r.request().postDataJSON();api.state.setupApplication={id:body.id,phase:'uncertain'};await r.abort();});
  await page.getByRole('button',{name:'确认并应用',exact:true}).click();
  await expect(page.getByRole('button',{name:'核对应用结果'})).toBeVisible();
  await page.getByRole('button',{name:'核对应用结果'}).click();expect(applyCount).toBe(1);
  await page.getByRole('button',{name:'放弃草稿并退出引导'}).click();
  await navigate(page,'下载管理');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间/})).toHaveValue('140');
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
});
test('manual SSE diagnostics restore saved results when cancellation is confirmed but response is lost',async({page})=>{
  const api=await mockAPI(page);
  Object.assign(api.diagnostics.calibration,{lastValid:{direct:{singleSpeedBPS:500000,fullSpeedBPS:2000000,fullConcurrency:8,fullCPUPercent:3,cpuP95Percent:5,cpuP99Percent:16.58,referenceBitrateBPS:2000000}}});
  await api.emit('calibration');
  await page.goto('/#/setup');await navigateStep(page,'3. 网络');
  await expect(page.getByRole('button',{name:'开始下载速度测试'})).toBeEnabled();
  await expect(page.getByText('16.00 Mbps',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'测速技术详情',exact:true}).click();
  const cpu=page.getByText('CPU 使用率：',{exact:false});
  await expect(cpu).toHaveText('CPU 使用率：平均 3.00% · p95 5.00%');
  await expect(page.getByLabel('正式下载全程的 CPU P95，不含视频准备')).toBeVisible();
  await page.getByRole('button',{name:'开始下载速度测试'}).click();
  await expect(page.getByRole('button',{name:'停止并放弃下载速度测试'})).toBeEnabled();
  Object.assign(api.diagnostics.calibration,{revision:3,fullCPUPercent:2,cpuP95Percent:4.89,cpuP99Percent:16.58});await api.emit('calibration');
  await expect(cpu).toHaveText('CPU 使用率：平均 2.00% · p95 4.89%');
  await page.route('**/api/setup/network/calibration/cancel',async r=>{Object.assign(api.diagnostics.calibration,{revision:10,running:false,phase:'canceled'});await api.emit('calibration');await r.abort();});
  await page.getByRole('button',{name:'停止并放弃下载速度测试'}).click();
  await expect(page.getByText('16.00 Mbps',{exact:true})).toBeVisible();
  await expect(cpu).toHaveText('CPU 使用率：平均 3.00% · p95 5.00%');
  await expect(page.getByRole('button',{name:'开始下载速度测试'})).toBeEnabled();
  expect(api.calls.filter(c=>c.path==='setup/network/calibration/start')).toHaveLength(1);
});
test('diagnostic upstream and completed results survive wizard step changes',async({page})=>{
  const api=await mockAPI(page);
  const directConnection=[{operation:'catalog',state:'available',latencyMS:11}];
  const proxyConnection=[{operation:'catalog',state:'available',latencyMS:42}];
  const directSpeed={singleSpeedBPS:250000,fullSpeedBPS:1000000,fullConcurrency:4,referenceBitrateBPS:2000000};
  const proxySpeed={singleSpeedBPS:500000,fullSpeedBPS:2000000,fullConcurrency:8,referenceBitrateBPS:2000000};
  await page.goto('/#/setup');await navigateStep(page,'3. 网络');
  const upstream=page.getByRole('combobox',{name:'检测上游',exact:true});
  await expect(upstream).toBeEnabled();await upstream.selectOption('proxy');
  await page.getByRole('button',{name:'开始连通性检测'}).click();
  await expect(page.getByRole('button',{name:'停止并放弃连通性检测'})).toBeEnabled();
  Object.assign(api.diagnostics.connectivity,{revision:3,running:false,phase:'completed',results:proxyConnection,lastValid:{direct:directConnection,proxy:proxyConnection}});
  await api.emit('connectivity');
  await page.getByRole('button',{name:'开始下载速度测试'}).click();
  await expect(page.getByRole('button',{name:'停止并放弃下载速度测试'})).toBeEnabled();
  Object.assign(api.diagnostics.calibration,{revision:3,running:false,phase:'completed',...proxySpeed,lastValid:{direct:directSpeed,proxy:proxySpeed}});
  await api.emit('calibration');await expect(upstream).toBeEnabled();
  await expect(page.getByText('16.00 Mbps',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'下一步',exact:true}).click();
  await expect(page.getByRole('heading',{name:'启用',exact:true})).toBeVisible();
  await page.getByRole('button',{name:'上一步',exact:true}).click();
  await expect(upstream).toHaveValue('proxy');
  await expect(page.getByText('歌曲列表：可连接 · 估计响应时间 42 ms',{exact:true})).toBeVisible();
  await expect(page.getByText('16.00 Mbps',{exact:true})).toBeVisible();
  await upstream.selectOption('direct');
  await navigateStep(page,'2. 存储');await navigateStep(page,'3. 网络');
  await expect(upstream).toHaveValue('direct');
  await expect(page.getByText('歌曲列表：可连接 · 估计响应时间 11 ms',{exact:true})).toBeVisible();
  await expect(page.getByText('8.00 Mbps',{exact:true})).toBeVisible();
  const starts=api.calls.filter(c=>c.path.startsWith('setup/network/')&&c.path.endsWith('/start'));
  expect(starts.map(c=>({path:c.path,upstream:c.body.upstream}))).toEqual([
    {path:'setup/network/connectivity/start',upstream:'proxy'},
    {path:'setup/network/calibration/start',upstream:'proxy'},
  ]);
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
  const primary=page.getByRole('button',{name:'确认并应用',exact:true});
  const before=await primary.boundingBox();
  await page.getByRole('button',{name:'刷新接入检查',exact:true}).click();
  await expect(page.getByText('最终应用成功后按自动加速设置配置游戏连接；可能需要管理员授权。',{exact:true})).toBeVisible();
  expect(await primary.boundingBox()).toEqual(before);
  await expect(primary).toBeInViewport({ratio:1});
  await page.screenshot({path:info.outputPath('global-feedback.png'),animations:'disabled'});
  await navigateStep(page,'1. 开始');await page.getByRole('button',{name:'查看完整条款',exact:true}).click();
  for(let i=0;i<8;i++){await page.keyboard.press('Tab');expect(await page.getByRole('dialog').evaluate(el=>el.contains(document.activeElement))).toBe(true);}
  await page.keyboard.press('Escape');await expect(page.getByRole('dialog')).toHaveCount(0);
});

for(const startup of [false,true])test(`confirmation explains service and queue actions before application (startup=${startup})`,async({page})=>{
  const api=await mockAPI(page);
  api.setupEffects.restartRequired=startup;api.setupEffects.applyStartup=startup;
  api.state.queue={running:false,desired:false};
  api.state.hosts.ready=true;
  await page.goto('/#/setup');await navigateStep(page,'4. 启用');
  await expect(page.getByText('本地服务：'+(startup?'将启动':'保持关闭'),{exact:true})).toBeVisible();
  await expect(page.getByText('房间队列预加载：'+(startup?'随网关开启':'保持关闭'),{exact:true})).toBeVisible();
  await expect(page.getByText(/最终应用成功后按自动加速设置配置游戏连接/)).toBeVisible();
  await expect(page.getByText(/条款确认已即时生效/)).toBeVisible();
  expect(api.calls.some(call=>call.path==='setup/apply')).toBe(false);
  await page.getByRole('button',{name:'确认并应用',exact:true}).click();
  await expect(page.getByText(startup?'配置已应用。加速运行中；房间队列预加载已开启。':'配置已应用。加速未运行；房间队列预加载已关闭。',{exact:true})).toBeVisible();
});

test('failed review blocks confirmation until the effects have been checked',async({page})=>{
  const api=await mockAPI(page);let fail=true;
  await page.route('**/api/setup/validate',r=>fail?r.fulfill({status:400,json:{error:'配置版本已改变'}}):r.fulfill({json:api.setupEffects}));
  await page.goto('/#/setup');await navigateStep(page,'4. 启用');
  await expect(page.getByText(/无法核对完成后的动作/)).toBeVisible();
  await expect(page.getByRole('button',{name:'确认并应用',exact:true})).toBeDisabled();
  fail=false;await page.getByRole('button',{name:'刷新接入检查',exact:true}).click();
  await expect(page.getByRole('button',{name:'确认并应用',exact:true})).toBeEnabled();
  expect(api.calls.some(call=>call.path==='setup/apply')).toBe(false);
});
