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
  api.state.setupNetwork={monitor:{results:[catalog,{...catalog,operation:'playback_url',route:'cf'}]}};
  await page.setViewportSize({width:390,height:844});await page.goto('/#/monitor');
  const panel=page.locator('#network');
  for(const label of ['歌曲列表','播放地址解析','视频资源加载'])await panel.getByRole('button',{name:label,exact:true}).click();
  const details=panel.getByRole('button',{name:'线路技术详情',exact:true});
  for(const button of await details.all())await button.click();
  const row=(route:string)=>panel.getByRole('grid',{name:'线路检测：'+route,exact:true});
  await expect(row('api')).toContainText('清单版本：fixture-v1');
  await expect(row('api')).toContainText('样本吞吐 512.0 KiB/s · 测速时间');
  await expect(row('api')).toContainText('下载 2.00 s / 1.00 MiB · 歌曲 #42 · HTTP 200');
  await expect(row('cf')).toContainText('样本吞吐 0.0 KiB/s');
  await expect(row('cf')).toContainText('下载 2.00 s / 0.00 MiB');
  await expect(row('failed.example')).toContainText('尚无成功样本 · 当前资源检查失败：请求超时');
  await expect(row('missing.example')).toContainText('尚无成功样本 · 未取得该域名的有效资源地址');
  await expect(row('waiting.example')).toContainText('尚无成功样本 · 业务正在加载视频，自动测速已暂停');
  await expect(row('history.example')).toContainText('吞吐样本：512.0 KiB/s · 歌曲 #7 · 测速时间');
  await expect(row('history.example')).toContainText('历史样本（已过期）');
  await expect(row('history.example')).toContainText('地址来源：api · node=cf · API 通道 source-1');
  expect((await row('history.example').innerText()).match(/API 通道 source-1/g)).toHaveLength(1);
  await page.getByRole('button',{name:'直连诊断详情',exact:true}).click();
  const diagnostic=page.getByRole('grid',{name:'直连诊断结果'});
  await expect(diagnostic).toContainText('歌曲列表 · api');
  await expect(diagnostic).toContainText('播放地址解析 · cf');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await panel.screenshot({path:info.outputPath('network-details.png')});
  expect(api.calls.some(c=>c.path.startsWith('upstream/check')||c.path.endsWith('/start'))).toBe(false);
});
