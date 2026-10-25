import {navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('manual SSE diagnostics restore saved results when cancellation is confirmed but response is lost',async({page})=>{
  const api=await mockAPI(page);
  Object.assign(api.diagnostics.calibration,{lastValid:{direct:{singleSpeedBPS:500000,fullSpeedBPS:2000000,fullConcurrency:8,fullCPUPercent:3,cpuP95Percent:5,cpuP99Percent:16.58,referenceBitrateBPS:2000000}}});
  await api.emit('calibration');
  await page.goto('/#/network');
  await expect(page.getByRole('button',{name:'开始下载速度测试'})).toBeEnabled();
  await expect(page.getByText('16.00 Mbps',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'测速技术详情',exact:true}).click();
  const cpu=page.getByText('CPU 使用率：',{exact:false});
  await expect(cpu).toHaveText('CPU 使用率：平均 3.00% · p95 5.00%');
  await expect(page.getByLabel('正式下载全程的 CPU P95，不含视频准备')).toBeVisible();
  await page.getByRole('button',{name:'开始下载速度测试'}).click();
  await expect(page.getByRole('button',{name:'停止并放弃下载速度测试'})).toBeEnabled();
  Object.assign(api.diagnostics.calibration,{revision:3,fullCPUPercent:2,cpuP95Percent:4.89,cpuP99Percent:16.58});await api.emit('calibration');
  await page.getByRole('button',{name:'测速技术详情',exact:true}).click();await expect(cpu).toHaveText('CPU 使用率：平均 2.00% · p95 4.89%');
  await page.route('**/api/network/calibration/cancel',async r=>{Object.assign(api.diagnostics.calibration,{revision:10,running:false,phase:'canceled'});await api.emit('calibration');await r.abort();});
  await page.getByRole('button',{name:'停止并放弃下载速度测试'}).click();
  await expect(page.getByText('16.00 Mbps',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'测速技术详情',exact:true}).click();await expect(cpu).toHaveText('CPU 使用率：平均 3.00% · p95 5.00%');
  await expect(page.getByRole('button',{name:'开始下载速度测试'})).toBeEnabled();
  expect(api.calls.filter(c=>c.path==='network/calibration/start')).toHaveLength(1);
});

test('diagnostic upstream and completed results survive page changes',async({page})=>{
  const api=await mockAPI(page);
  const directConnection=[{operation:'catalog',state:'available',latencyMS:11}];
  const proxyConnection=[{operation:'catalog',state:'available',latencyMS:42}];
  const directSpeed={singleSpeedBPS:250000,fullSpeedBPS:1000000,fullConcurrency:4,referenceBitrateBPS:2000000};
  const proxySpeed={singleSpeedBPS:500000,fullSpeedBPS:2000000,fullConcurrency:8,referenceBitrateBPS:2000000};
  await page.goto('/#/network');
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
  await navigate(page,'加速');await navigate(page,'网络');
  await expect(upstream).toHaveValue('proxy');
  await expect(page.getByText('歌曲列表：可连接 · 估计响应时间 42 ms',{exact:true})).toBeVisible();
  await expect(page.getByText('16.00 Mbps',{exact:true})).toBeVisible();
  await upstream.selectOption('direct');
  await navigate(page,'曲库');await navigate(page,'网络');
  await expect(upstream).toHaveValue('direct');
  await expect(page.getByText('歌曲列表：可连接 · 估计响应时间 11 ms',{exact:true})).toBeVisible();
  await expect(page.getByText('8.00 Mbps',{exact:true})).toBeVisible();
  const starts=api.calls.filter(c=>c.path.startsWith('network/')&&c.path.endsWith('/start'));
  expect(starts.map(c=>({path:c.path,upstream:c.body.upstream}))).toEqual([
    {path:'network/connectivity/start',upstream:'proxy'},
    {path:'network/calibration/start',upstream:'proxy'},
  ]);
});
