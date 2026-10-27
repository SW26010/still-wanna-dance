import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import type {HealthResult} from '../src/types';

test('network details preserve sample evidence and distinguish failures from missing samples',async({page},info)=>{
  const api=await mockAPI(page);
  const catalog:HealthResult={operation:'catalog',route:'api',entry:'catalog.example',mode:'direct',state:'available',channelID:'direct/catalog',ip:'192.0.2.1',
    catalogTime:'fixture-v1',estimatedLatencyMS:12.5,estimatedSpeedBPS:524288,throughputObservedAt:'2026-10-16T00:00:00Z',transferDurationMS:2000,transferredBytes:1048576,sampleSongID:42,http:200};
  const resource=(route:string,patch:Partial<HealthResult>):HealthResult=>({operation:'resource',route,entry:route,mode:'direct',state:'unknown',...patch});
  api.state.upstreamMonitor={throughputStatus:'business_busy',results:[catalog,
    {...catalog,operation:'playback_url',route:'cf',entry:'playback.example',estimatedSpeedBPS:0,transferredBytes:0},
    resource('failed.example',{state:'unavailable',reason:'timeout'}),
    resource('missing.example',{reason:'resolution_unavailable'}),
    resource('waiting.example',{}),
    resource('history.example',{state:'unavailable',reason:'timeout',lastThroughput:{bytes:1048576,durationNs:2e9,songID:7,observedAt:'2000-01-01T00:00:00Z'},
      sources:[{api:'api',node:'cf',apiChannelID:'source-1'},{api:'api',node:'cf',apiChannelID:'source-1'}]}),
  ]};
  await page.setViewportSize({width:390,height:844});await page.goto('/#/network');
  const panel=page.locator('#network');
  const rows=panel.getByRole('table').getByRole('row').filter({has:page.getByRole('button',{name:'线路技术详情',exact:true})});
  const expected=['清单版本：fixture-v1','0.0 KiB/s','尚无成功样本 · 当前资源检查失败：请求超时','尚无成功样本 · 未取得该域名的有效资源地址','尚无成功样本 · 业务正在加载视频，自动测速已暂停','历史样本（已过期）'];
  for(let i=0;i<expected.length;i++){
    await rows.nth(i).getByRole('button',{name:'线路技术详情',exact:true}).click();
    const detail=page.locator('.fui-PopoverSurface:visible');await expect(detail).toContainText(expected[i]);
    if(i===0){await expect(detail).toContainText('512.0 KiB/s');await expect(detail).toContainText('2.00 s / 1.00 MiB');await expect(detail).toContainText('HTTP 200');}
    if(i===5){await expect(detail).toContainText('歌曲 #7');expect((await detail.innerText()).match(/API 通道 source-1/g)).toHaveLength(1);}
    await page.keyboard.press('Escape');
  }
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await panel.screenshot({path:info.outputPath('network-details.png'),animations:'disabled'});
  expect(api.calls.some(c=>c.path.startsWith('upstream/check')||c.path.endsWith('/start'))).toBe(false);
});

test('unmeasured network offers direct checks and shows new samples without expanding a section',async({page},info)=>{
  const {state,calls}=await mockAPI(page);await page.clock.install();await page.goto('/#network');
  const panel=page.locator('#network');
  await expect(panel.getByText('未检测',{exact:true})).toHaveCount(3);
  await expect(panel).not.toContainText('暂无检测结果');await expect(panel).not.toContainText('最近完成：—');
  await panel.getByRole('button',{name:'检测视频列表',exact:true}).click();
  await expect.poll(()=>calls.filter(c=>c.path==='upstream/check/catalog').length).toBe(1);
  state.upstreamMonitor={results:[{operation:'catalog',route:'api',entry:'catalog.example',mode:'direct',state:'available',estimatedLatencyMS:12.5}]};
  await page.clock.fastForward(2500);
  await expect(panel.getByRole('table',{name:'线路检测：catalog',exact:true})).toContainText('12.5 ms');
  await expect(panel.getByText('未检测',{exact:true})).toHaveCount(2);
  await panel.screenshot({path:info.outputPath('network-partial-results.png'),animations:'disabled'});
});
