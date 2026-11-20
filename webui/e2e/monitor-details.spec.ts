import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import type {HealthResult} from '../src/types';

test('connection tables have no horizontal scroll range when their columns fit',async({page},info)=>{
  const {state}=await mockAPI(page);
  state.upstreamMonitor={results:(['catalog','playback_url','resource'] as const).map(operation=>({operation,route:'api',entry:'catalog.example',mode:'direct',state:'available',ip:'192.0.2.1',estimatedLatencyMS:12.5,estimatedSpeedBPS:524288}))};
  await page.setViewportSize({width:1280,height:900});await page.goto('/#/network');
  const panel=page.locator('#network'),regions=panel.getByRole('region',{name:/线路检测：/});
  await expect(regions).toHaveCount(2);
  for(const width of [900,1024,1280,1920,2560]){
    await page.setViewportSize({width,height:900});
    expect(await regions.evaluateAll(elements=>elements.map(element=>element.scrollWidth-element.clientWidth)),`${width}px connection tables`).toEqual([0,0]);
    if(width===1280)await panel.screenshot({path:info.outputPath('connection-tables-wide.png'),animations:'disabled'});
  }
  await page.setViewportSize({width:390,height:844});await page.emulateMedia({colorScheme:'dark'});
  expect(await regions.evaluateAll(elements=>elements.every(element=>element.scrollWidth>element.clientWidth))).toBe(true);
  await regions.evaluateAll(elements=>{for(const element of elements)element.scrollLeft=element.scrollWidth;});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  const trigger=panel.getByRole('button',{name:'线路技术详情',exact:true}).last(),detail=page.getByRole('group',{name:'线路技术详情',exact:true});
  await trigger.hover();await expect(detail).toContainText('catalog.example');
  await detail.hover();await expect(detail).toBeVisible();
  await expect(detail).toBeInViewport({ratio:1});
  await page.screenshot({path:info.outputPath('connection-details-hover-dark.png'),animations:'disabled'});
  await page.mouse.move(0,0);await expect(detail).toBeHidden();
  await trigger.focus();await page.keyboard.press('Enter');await expect(detail).toBeVisible();
  await page.keyboard.press('Escape');await expect(detail).toBeHidden();await expect(trigger).toBeFocused();
  await panel.screenshot({path:info.outputPath('connection-tables-narrow-dark.png'),animations:'disabled'});
});

test('catalog and playback retain their diagnostics while resource rows use shared observations only',async({page},info)=>{
  const api=await mockAPI(page);
  const catalog:HealthResult={operation:'catalog',route:'api',entry:'catalog.example',mode:'direct',state:'available',channelID:'direct/catalog',ip:'192.0.2.1',
    catalogTime:'fixture-v1',estimatedLatencyMS:12.5,estimatedSpeedBPS:524288,throughputObservedAt:'2026-10-16T00:00:00Z',transferDurationMS:2000,transferredBytes:1048576,sampleSongID:42,http:200};
  const resource=(route:string,patch:Partial<HealthResult>):HealthResult=>({operation:'resource',route,entry:route,mode:'direct',state:'unknown',...patch});
  api.state.upstreamMonitor={results:[catalog,
    {...catalog,operation:'playback_url',route:'cf',entry:'playback.example',estimatedSpeedBPS:0,transferredBytes:0},
    resource('failed.example',{state:'unavailable',reason:'timeout'}),
    resource('missing.example',{reason:'resolution_unavailable'}),
    resource('waiting.example',{}),
    resource('history.example',{state:'unavailable',reason:'timeout',
      sources:[{api:'api',node:'cf',apiChannelID:'source-1'},{api:'api',node:'cf',apiChannelID:'source-1'}]}),
  ]};
  await page.setViewportSize({width:390,height:844});await page.goto('/#/network');
  const panel=page.locator('#network');
  const rows=panel.getByRole('table').getByRole('row').filter({has:page.getByRole('button',{name:'线路技术详情',exact:true})});
  await expect(rows).toHaveCount(2);
  const section=panel.locator('section').filter({has:page.getByRole('heading',{name:'视频资源加载'})});
  await expect(section.getByRole('button',{name:'立即采样'})).toBeVisible();
  await expect(section.getByRole('table')).toHaveCount(0);
  await expect(section).toContainText('暂无下载或采样记录');
  await expect(panel.getByRole('status')).toContainText('视频下载：尚待观测');
  const expected=['清单版本：fixture-v1','0.0 KiB/s'];
  for(let i=0;i<expected.length;i++){
    await rows.nth(i).getByRole('button',{name:'线路技术详情',exact:true}).hover();
    const detail=page.locator('.fui-PopoverSurface:visible');await expect(detail).toContainText(expected[i]);
    if(i===0){await expect(detail).toContainText('512.0 KiB/s');await expect(detail).toContainText('2.00 s / 1.00 MiB');await expect(detail).toContainText('HTTP 200');}
    await detail.hover();await expect(detail).toBeVisible();
    await page.mouse.move(0,0);await expect(detail).toBeHidden();
  }
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await panel.screenshot({path:info.outputPath('network-details.png'),animations:'disabled'});
  expect(api.calls.some(c=>c.path.startsWith('upstream/check')||c.path.endsWith('/start'))).toBe(false);
});

test('unmeasured network offers direct checks and shows new samples without expanding a section',async({page},info)=>{
  const {state,calls}=await mockAPI(page);await page.clock.install({time:new Date('2026-10-30T00:00:00Z')});await page.goto('/#network');
  const panel=page.locator('#network');
  await expect(panel.getByText('未检测',{exact:true})).toHaveCount(2);
  await expect(panel).not.toContainText('暂无检测结果');await expect(panel).not.toContainText('最近完成：—');
  // Finish the action-triggered refresh before publishing the later sample.
  const actionRefresh=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/status');
  await panel.getByRole('button',{name:'检测歌曲列表',exact:true}).click();
  await expect.poll(()=>calls.filter(c=>c.path==='upstream/check/catalog').length).toBe(1);
  await (await actionRefresh).finished();
  await expect(page.getByRole('listitem',{name:'网络检测已开始，请查看检测结果。',exact:true})).toBeVisible();
  // Pin a future virtual time relative to install, avoiding a wall-clock race
  // between reading Date.now() in the page and issuing pauseAt from Node.
  await page.clock.pauseAt(new Date('2026-10-30T00:01:00Z'));
  state.upstreamMonitor={results:[{operation:'catalog',route:'api',entry:'catalog.example',mode:'direct',state:'available',estimatedLatencyMS:12.5}]};
  const reads=calls.filter(c=>c.path==='status').length;
  // Drive the real five-second polling cycle and wait for its request; a
  // shorter clock jump races the action-triggered refresh and real time.
  await expect.poll(async()=>{await page.clock.fastForward(5000);return calls.filter(c=>c.path==='status').length;}).toBeGreaterThan(reads);
  await expect(panel.getByRole('table',{name:'线路检测：catalog',exact:true})).toContainText('12.5 ms');
  await expect(panel.getByText('未检测',{exact:true})).toHaveCount(1);
  await panel.screenshot({path:info.outputPath('network-partial-results.png'),animations:'disabled'});
});

test('resource observations show shared downloads and samples per IP',async({page})=>{
  const {state,calls}=await mockAPI(page);
  const at=new Date().toISOString();
  state.downloadRoutes=[
    {host:'video.example',ip:'192.0.2.1',observedAt:at,source:'download',blockAt:at,blockDurationNs:1250000000},
    {host:'video.example',ip:'2001:db8::1',observedAt:at,source:'sample',failed:true},
  ];
  await page.goto('/#/network');
  const rows=page.getByRole('table',{name:'资源下载线路'}).getByRole('row');
  await expect(rows.nth(1)).toContainText('下载成功');
  await expect(rows.nth(1)).toContainText('1.25 秒');
  await expect(rows.nth(2)).toContainText('2001:db8::1');
  await expect(rows.nth(2)).toContainText('采样失败');
  await page.getByRole('button',{name:'立即采样'}).click();
  await expect.poll(()=>calls.filter(c=>c.path==='upstream/check/throughput').length).toBe(1);
  await expect(page.getByText('资源采样已完成。',{exact:true})).toBeVisible();
  let release!:()=>void;
  const gate=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/upstream/check/throughput',async route=>{
    await gate;
    await route.fallback();
  });
  const sent=page.waitForRequest(r=>new URL(r.url()).pathname==='/api/upstream/check/throughput');
  await rows.nth(2).getByRole('button',{name:'采样 video.example 2001:db8::1'}).click();
  try {
    expect((await sent).postDataJSON()).toEqual({host:'video.example',ip:'2001:db8::1'});
    await expect(page.getByRole('button',{name:'立即采样'})).toBeDisabled();
    await page.getByRole('button',{name:'检测歌曲列表'}).click();
    await expect.poll(()=>calls.filter(c=>c.path==='upstream/check/catalog').length).toBe(1);
  } finally { release(); }
  await expect(page.getByText('2001:db8::1 采样已完成。',{exact:true})).toBeVisible();
  await page.setViewportSize({width:390,height:844});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
