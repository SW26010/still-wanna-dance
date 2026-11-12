import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({locale:'zh-CN',viewport:{width:390,height:844}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(base+'/#network-tests');
  await expect(page.getByText('检测上游：SOCKS5',{exact:true})).toBeVisible();
  await expect(page.getByRole('link',{name:'当前连接',exact:true})).toHaveCount(0);
  await expect(page.getByRole('combobox',{name:'检测上游',exact:true})).toHaveCount(0);
  await expect(page.getByText(/^最后测试：/)).toHaveCount(1);
  const response=page.waitForResponse(r=>r.url().endsWith('/api/network/connectivity/start')&&r.request().method()==='POST');
  await page.getByRole('button',{name:'开始连通性检测',exact:true}).click();
  const started=await response;
  expect(started.ok()).toBe(true);
  expect(started.request().postDataJSON()).toEqual({});
  await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeEnabled();
  await expect(page.locator('.diagnostic-rows p')).toHaveCount(5);
  await expect(page.getByText('未使用（已有可用清单）',{exact:false})).toHaveCount(2);
  await expect(page.locator('.diagnostic-rows')).not.toContainText('连接失败');
  await expect(page.locator('.diagnostic-rows')).toContainText('响应时间');
  await expect(page.locator('.diagnostic-rows')).not.toContainText('估计');
  await expect(page.getByText(/^最后测试：/)).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.mouse.move(0,0);
  await page.getByRole('button',{name:'开始连通性检测',exact:true}).blur();
  await page.screenshot({path:join(output,'active-proxy.png'),fullPage:true});
  await page.goto(base+'/#/home');
  const home=page.locator('.upstream-card');
  await expect(home.getByText('SOCKS5',{exact:true})).toBeVisible();
  for(const name of ['歌曲列表','播放地址','视频下载'])await expect(home.getByRole('button',{name:new RegExp('检测'+name)})).toHaveText(new RegExp(name+'：\\d+ ms'));
  await page.evaluate(()=>new Promise((resolve,reject)=>{
    const stream=window.upstreamEvidenceStream=new EventSource('/api/network/connectivity/events');
    stream.addEventListener('connectivity',event=>{window.upstreamEvidence=JSON.parse(event.data);resolve();});
    stream.onerror=()=>reject(new Error('connectivity stream unavailable'));
  }));
  for(const [name,operation] of [['歌曲列表','catalog'],['播放地址','playback_url'],['视频下载','resource']]){
    const before=await page.evaluate(()=>window.upstreamEvidence);
    const response=page.waitForResponse(r=>r.url().endsWith('/api/network/connectivity/start')&&r.request().method()==='POST');
    await home.getByRole('button',{name:new RegExp('检测'+name)}).click();
    const started=await response;
    expect(started.ok()).toBe(true);expect(started.request().postDataJSON()).toEqual({operation});
    await expect.poll(()=>page.evaluate(()=>window.upstreamEvidence)).toMatchObject({run:before.run+1,operation,running:false,phase:'completed'});
    const after=await page.evaluate(()=>window.upstreamEvidence);
    expect(after.results.filter(row=>row.operation!==operation)).toEqual(before.results.filter(row=>row.operation!==operation));
    expect(after.nextCheck).toBe(before.nextCheck);
  }
  const before=await page.evaluate(()=>window.upstreamEvidence);
  const homeResponse=page.waitForResponse(r=>r.url().endsWith('/api/network/connectivity/start')&&r.request().method()==='POST');
  await home.getByRole('button',{name:'重新检测上游',exact:true}).click();
  const homeStarted=await homeResponse;
  expect(homeStarted.ok()).toBe(true);expect(homeStarted.request().postDataJSON()).toEqual({});
  await expect.poll(()=>page.evaluate(()=>window.upstreamEvidence)).toMatchObject({run:before.run+1,running:false,phase:'completed'});
  const after=await page.evaluate(()=>window.upstreamEvidence);
  for(const operation of ['catalog','playback_url','resource'])expect(after.results.some(row=>row.operation===operation&&row.state==='available')).toBe(true);
  await page.evaluate(()=>window.upstreamEvidenceStream.close());
  await home.getByRole('link',{name:'上游',exact:true}).click();
  await expect(page).toHaveURL(/#network-tests$/);
  await expect(page.getByRole('heading',{name:'连通性检测',exact:true})).toBeVisible();
  await page.goto(base+'/#/library');
  await expect(page.getByRole('list',{name:'曲库封面'})).toContainText('canonical Udon metadata');
  expect(errors).toEqual([]);
  await writeFile(join(output,'result.json'),JSON.stringify({passed:true,realConsole:true,upstream:'controlled HTTP fixture',checks:['all three home actions','home full refresh','shared results and automatic deadline','home title navigation'],screenshots:['active-proxy.png'],errors},null,2));
} finally {await browser.close();}
