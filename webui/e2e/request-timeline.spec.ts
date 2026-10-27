import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

const record=(id:number)=>({id,at:Date.now()-id*1000,completedAt:Date.now()-id*1000+800,resource:'a'.repeat(32),songID:id===1?'42':'',method:'GET',range:'bytes=0-99',elapsedMS:800,bytes:id===2?0:50,responseBytes:id===2?null:100,status:id===2?502:206,cache:'MISS',outcome:id===2?'failed':'canceled',firstBodyNS:id===2?null:10000000,firstBodyBlocking:id===2?null:true,songs:[{id:'999',title:'当前关联不是真实请求身份'}]});

for(const [width,scheme] of [[1280,'light'],[390,'dark'],[320,'light']] as const)test(`response sources use frozen bytes and fit ${width}px ${scheme}`,async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:scheme,forcedColors:width===320?'active':'none'});
  const sources={downloadBytes:1048576,cacheBytes:2097152,localBytes:1048576};
  await page.route('**/api/response-sources',r=>r.fulfill({json:sources}));
  await page.route('**/api/history?*',r=>r.fulfill({json:{storageID:'fixture',requests:[{...record(1),sources},{...record(2),sources:{downloadBytes:0,cacheBytes:0,localBytes:0}},record(3)],hasMore:false,nextCursor:''}}));
  await page.goto('/#/timeline');
  const summary=page.getByRole('region',{name:'响应来源统计'});
  await expect(summary.getByText('下载 25%',{exact:true})).toBeVisible();
  await expect(summary.getByText('缓存 50%',{exact:true})).toBeVisible();
  await expect(summary.getByText('本地视频 25%',{exact:true})).toBeVisible();
  const row=page.getByRole('row').filter({has:page.getByRole('link',{name:'ID 42',exact:true})});
  await expect(row).toContainText('下载 25%');await expect(row).toContainText('缓存 50%');
  await page.getByRole('button',{name:'全部保留期',exact:true}).click();await expect(summary).toContainText('缓存 50%');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.evaluate(()=>scrollTo(0,0));
  await page.screenshot({path:info.outputPath('response-sources.png'),fullPage:true,animations:'disabled'});
});

test('source refresh retains data and reports failures without showing zero',async({page})=>{
  await mockAPI(page);let fail=false;
  await page.route('**/api/response-sources',r=>fail?r.fulfill({status:500,body:'failed'}):r.fulfill({json:{downloadBytes:8,cacheBytes:0,localBytes:0}}));
  await page.goto('/#/timeline');const summary=page.getByRole('region',{name:'响应来源统计'});
  await expect(summary).toContainText('下载 100%');fail=true;
  await page.getByRole('button',{name:'刷新来源统计',exact:true}).click();
  await expect(summary).toContainText('来源统计读取失败，请重试。');await expect(summary).toContainText('下载 100%');
});

for(const populated of [false,true])test(`automatic history refresh preserves visible results and controls (${populated?'rows':'empty'})`,async({page})=>{
  const api=await mockAPI(page);await page.clock.install();let reads=0,latencyReads=0,release!:()=>void;
  const pending=new Promise<void>(resolve=>{release=resolve;});
  const requests=populated?[record(1)]:[];
  await page.route('**/api/history?*',async route=>{reads++;const refresh=reads>1;if(refresh)await pending;await route.fulfill({json:{storageID:'fixture',requests:refresh?[...requests,record(3)]:requests,hasMore:false,nextCursor:''}});});
  await page.route('**/api/latency',async route=>{latencyReads++;if(latencyReads>1)await pending;await route.fallback();});
  await page.goto('/#/timeline');
  const result=populated?page.getByRole('link',{name:'ID 42',exact:true}):page.getByText('该时间范围内暂无请求记录。',{exact:true});
  await expect(result).toBeVisible();
  const table=page.getByRole('table',{name:'请求历史列表',exact:true}),position=await table.boundingBox();
  await expect(page.getByRole('button',{name:'刷新首正文统计',exact:true})).toBeEnabled();
  await page.clock.fastForward(30100);await expect.poll(()=>latencyReads).toBe(2);expect(reads).toBe(1);
  await api.historyChanged();await expect.poll(()=>reads).toBe(2);
  await expect(result).toBeVisible({timeout:1000});
  await expect(page.getByRole('button',{name:'刷新本页',exact:true})).toHaveCount(0);
  await expect(page.getByRole('checkbox',{name:'暂停请求刷新',exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'按收到时间排序',exact:true})).toBeEnabled({timeout:1000});
  await expect(page.getByRole('button',{name:'刷新首正文统计',exact:true})).toBeEnabled({timeout:1000});
  expect(await table.boundingBox()).toEqual(position);
  release();await expect(page.getByRole('button',{name:'请求 3 的明细',exact:true})).toBeVisible();
});

test('paging during a background refresh ignores its late response',async({page})=>{
  const api=await mockAPI(page);await page.clock.install();let reads=0,release!:()=>void,complete!:()=>void;
  const pending=new Promise<void>(resolve=>{release=resolve;}),finished=new Promise<void>(resolve=>{complete=resolve;});
  await page.route('**/api/history?*',async route=>{
    const next=new URL(route.request().url()).searchParams.get('cursor')==='next';
    if(next){await route.fulfill({json:{storageID:'fixture',requests:[record(3)],hasMore:false,nextCursor:''}});return;}
    reads++;const refresh=reads>1;
    if(refresh)await pending;
    await route.fulfill({json:{storageID:'fixture',requests:[record(refresh?2:1)],hasMore:true,nextCursor:'next'}}).catch(()=>{});
    if(refresh)complete();
  });
  await page.goto('/#/timeline');await expect(page.getByRole('button',{name:'请求 1 的明细',exact:true})).toBeVisible();
  await api.historyChanged();await expect.poll(()=>reads).toBe(2);
  await page.getByRole('button',{name:'下一页历史',exact:true}).click();
  await expect(page.getByRole('button',{name:'请求 3 的明细',exact:true})).toBeVisible();
  release();await finished;
  await expect(page.getByText('第 2 页 · 1 条',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'请求 2 的明细',exact:true})).toHaveCount(0);
});

test('history events coalesce during reads without refreshing summaries or losing focus',async({page})=>{
  const api=await mockAPI(page);let reads=0,release!:()=>void;
  const pending=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/history?*',async route=>{
    const read=++reads;
    if(read===2)await pending;
    await route.fulfill({json:{storageID:'fixture',requests:read<3?[record(1)]:[record(1),record(3)],hasMore:false,nextCursor:''}});
  });
  await page.goto('/#/timeline');
  const details=page.getByRole('button',{name:'请求 1 的明细',exact:true});
  await details.click();await expect(page.getByRole('group',{name:'请求 1 的明细',exact:true})).toBeVisible();
  const summaryReads=api.calls.filter(c=>['latency','response-sources'].includes(c.path)).length;
  await api.historyChanged();await expect.poll(()=>reads).toBe(2);
  for(let i=0;i<8;i++)await api.historyChanged();
  expect(reads).toBe(2);
  release();await expect(page.getByRole('button',{name:'请求 3 的明细',exact:true})).toBeVisible();
  expect(reads).toBe(3);
  await expect(page.getByRole('group',{name:'请求 1 的明细',exact:true})).toBeVisible();
  await expect(details).toBeFocused();
  expect(api.calls.filter(c=>['latency','response-sources'].includes(c.path))).toHaveLength(summaryReads);
});

test('history reconnects and resumes with fresh data, releases hidden subscriptions, and can retry a failed query',async({page})=>{
  const api=await mockAPI(page);let id=1,fail=false,reads=0;
  await page.route('**/api/history?*',route=>{reads++;return fail?route.fulfill({status:500,body:'failed'}):route.fulfill({json:{storageID:'fixture',requests:[record(id)],hasMore:false,nextCursor:''}});});
  const streams=async()=>((await(await page.request.get('/__fixture/streams?id='+api.fixtureID)).json()).history||0);
  await page.goto('/#/timeline');await expect(page.getByRole('button',{name:'请求 1 的明细',exact:true})).toBeVisible();
  await expect.poll(streams).toBe(1);
  id=3;
  await page.request.post('/__fixture/disconnect',{data:{id:api.fixtureID,kind:'history'}});
  await expect(page.getByRole('listitem').getByText('历史更新连接已断开，正在重新连接…',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'请求 3 的明细',exact:true})).toBeVisible({timeout:10000});
  await expect(page.getByRole('listitem').getByText('历史更新连接已断开，正在重新连接…',{exact:true})).toHaveCount(0);
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,get:()=>true});document.dispatchEvent(new Event('visibilitychange'));});
  await expect.poll(streams).toBe(0);
  id=4;const before=reads;await api.historyChanged();expect(reads).toBe(before);
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,get:()=>false});document.dispatchEvent(new Event('visibilitychange'));});
  await expect(page.getByRole('button',{name:'请求 4 的明细',exact:true})).toBeVisible();
  await expect.poll(streams).toBe(1);
  await navigate(page,'设置');await expect.poll(streams).toBe(0);
  id=5;await api.historyChanged();await navigate(page,'历史');
  await expect(page.getByRole('button',{name:'请求 5 的明细',exact:true})).toBeVisible();
  fail=true;await api.historyChanged();
  await expect(page.getByLabel('历史查询结果',{exact:true})).toContainText('历史读取失败，请重新查询。');
  await expect(page.getByRole('button',{name:'请求 5 的明细',exact:true})).toHaveCount(0);
  fail=false;await page.getByRole('button',{name:'查询历史',exact:true}).click();
  await expect(page.getByRole('button',{name:'请求 5 的明细',exact:true})).toBeVisible();
  await expect.poll(streams).toBe(1);
});

test('history can retry a rejected event connection without promising automatic recovery',async({page})=>{
  await mockAPI(page);let fail=true;
  await page.route('**/api/history/events',route=>fail?route.fulfill({status:503,body:'unavailable'}):route.fallback());
  await page.goto('/#/timeline');
  await expect(page.getByRole('listitem').getByText('历史更新连接不可用，请重新查询。',{exact:true})).toBeVisible();
  await expect(page.getByRole('progressbar',{name:'正在读取历史'})).toHaveCount(0);
  fail=false;await page.getByRole('button',{name:'查询历史',exact:true}).click();
  await expect(page.getByText('该时间范围内暂无请求记录。',{exact:true})).toBeVisible();
  await expect(page.getByRole('listitem').getByText('历史更新连接不可用，请重新查询。',{exact:true})).toHaveCount(0);
});

test('history pages beyond 500 without growing the DOM; filtering leaves latency window alone',async({page})=>{
  await mockAPI(page);const queries:URLSearchParams[]=[];let latencyReads=0;
  await page.route('**/api/history?*',r=>{const q=new URL(r.request().url()).searchParams;queries.push(q);const index=Number(q.get('cursor')||0);return r.fulfill({json:{storageID:'fixture',requests:Array.from({length:50},(_,i)=>record(index*50+i+1)),hasMore:index<12,nextCursor:String(index+1)}});});
  await page.route('**/api/latency',r=>{latencyReads++;return r.fulfill({json:{from:Date.now()-72*3600000,to:Date.now(),availableFrom:Date.now()-3600000,partial:true,blocking:{samples:20,p50MS:10,p95MS:80},ready:{samples:0,p50MS:null,p95MS:null}}});});
  await page.clock.install();await page.goto('/#recent');
  await expect(page.getByRole('heading',{level:1,name:'历史'})).toBeVisible();
  await expect(page.getByText('P50 10 ms · P95 80 ms',{exact:true})).toBeVisible();
  await page.getByRole('heading',{name:'首正文等待时间',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('最近 72 小时 · 数据不足 72 小时');
  await page.keyboard.press('Escape');
  await expect(page.getByRole('link',{name:'ID 42',exact:true})).toBeVisible();
  await expect(page.getByText('当前关联不是真实请求身份',{exact:false})).toHaveCount(0);
  await expect(page.getByRole('row').filter({hasText:'请求失败'}).getByText('0 B',{exact:true})).toBeVisible();
  for(let n=2;n<=12;n++){
    await page.getByRole('button',{name:'下一页历史',exact:true}).click();
    await expect(page.getByText(`第 ${n} 页 · 50 条`,{exact:true})).toBeVisible();
    await expect(page.getByRole('button',{name:/^请求 \d+ 的明细$/})).toHaveCount(50);
  }
  await expect(page.getByLabel('历史查询结果',{exact:true})).toBeFocused();
  const reads=queries.length;await page.clock.fastForward(5100);expect(queries).toHaveLength(reads);
  await page.getByRole('button',{name:'上一页历史',exact:true}).click();await expect(page.getByText('第 11 页 · 50 条',{exact:true})).toBeVisible();
  await page.getByLabel('开始',{exact:true}).fill('2026-10-21T01:00');await page.getByLabel('结束',{exact:true}).fill('2026-10-22T01:00');
  await page.getByRole('button',{name:'查询历史',exact:true}).click();await expect(page.getByText('第 1 页 · 50 条',{exact:true})).toBeVisible();
  expect(queries.at(-1)?.get('cursor')).toBe('');expect(Number(queries.at(-1)?.get('to'))-Number(queries.at(-1)?.get('from'))).toBe(86400000);expect(latencyReads).toBe(1);
  await page.getByRole('button',{name:'全部保留期',exact:true}).click();await expect.poll(()=>queries.at(-1)?.has('from')).toBe(false);
});

for(const [width,scheme] of [[1280,'light'],[390,'dark'],[320,'light']] as const)test(`timeline fits ${width}px ${scheme} and preserves unknown facts`,async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:scheme,forcedColors:width===320?'active':'none'});
  await page.route('**/api/history?*',r=>r.fulfill({json:{storageID:'fixture',requests:[record(1),record(2),{...record(3),completedAt:null,resource:'',outcome:'aborted'}],hasMore:false,nextCursor:''}}));
  await page.goto('/#/timeline');
  await expect(page.getByText('暂无数据',{exact:true})).toHaveCount(2);
  await expect(page.getByText('结束：未知',{exact:true})).toBeVisible();
  const outcome=page.getByText('传输中止（原因未确定）',{exact:true});
  await expect(outcome).toBeVisible();
  const resultCell=outcome.locator('xpath=ancestor::td');
  const labelBox=await outcome.boundingBox(),httpBox=await resultCell.getByText('HTTP 206',{exact:true}).boundingBox();
  expect(httpBox!.y).toBeGreaterThanOrEqual(labelBox!.y+labelBox!.height-1);
  const table=page.getByRole('table',{name:'请求历史列表',exact:true});
  for(const [label,description] of [['交付','已写出字节'],['来源','当前请求等过缺块']] as const){
    const target=table.getByRole('columnheader',{name:label,exact:true}).getByText(label,{exact:true});
    await target.hover();await expect(page.getByRole('tooltip')).toContainText(description);
    await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toHaveCount(0);
    await page.mouse.move(0,0);await target.focus();
    await expect(page.getByRole('tooltip')).toContainText(description);
    await expect(target).toHaveAccessibleDescription(new RegExp(description));
    await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toHaveCount(0);
  }
  await expect(table.getByRole('button',{name:/交付比例说明|请求来源说明/})).toHaveCount(0);
  await page.getByLabel('历史查询结果',{exact:true}).screenshot({path:info.outputPath('history-table.png'),animations:'disabled'});
  await page.getByRole('button',{name:'请求 1 的明细',exact:true}).click();
  await expect(page.getByLabel('请求 1 的明细',{exact:true}).getByText('10 ms · Blocking',{exact:true})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('timeline.png'),fullPage:true,animations:'disabled'});
});

test('timeline retains filters across navigation and rejects inverted date ranges',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/timeline');
  await page.getByLabel('开始',{exact:true}).fill('2026-10-23T01:00');await page.getByLabel('结束',{exact:true}).fill('2026-10-22T01:00');
  const count=api.calls.filter(c=>c.path==='history').length;
  await page.getByRole('button',{name:'查询历史',exact:true}).click();await expect(page.getByText('结束时间必须晚于开始时间。',{exact:true})).toBeVisible();expect(api.calls.filter(c=>c.path==='history')).toHaveLength(count);
  await navigate(page,'设置');await navigate(page,'历史');await expect(page.getByLabel('开始',{exact:true})).toHaveValue('2026-10-23T01:00');
});

for(const width of [320,1280])test(`date errors stay with their field without overlapping controls at ${width}px`,async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width,height:900});await page.goto('/#/timeline');
  const from=page.getByLabel('开始',{exact:true}),to=page.getByLabel('结束',{exact:true});
  const query=page.getByRole('button',{name:'查询历史',exact:true});
  for(const [name,input] of [['开始',from],['结束',to]] as const){
    const labelBox=await page.getByText(name,{exact:true}).boundingBox(),inputBox=await input.boundingBox();
    expect(labelBox!.x+labelBox!.width).toBeLessThanOrEqual(inputBox!.x);
    expect(Math.abs(labelBox!.y+labelBox!.height/2-inputBox!.y-inputBox!.height/2)).toBeLessThanOrEqual(1);
  }
  if(width===1280){
    const controls=await Promise.all([from,to,query,page.getByRole('button',{name:'全部保留期',exact:true})].map(control=>control.boundingBox()));
    for(const box of controls)expect(Math.abs(box!.y-controls[0]!.y)).toBeLessThanOrEqual(1);
  }
  await from.fill('2026-10-23T01:00');await to.fill('2026-10-22T01:00');
  await query.click();
  await expect(to).toHaveAttribute('aria-invalid','true');
  await expect(to).toHaveAccessibleDescription('结束时间必须晚于开始时间。');
  const message=page.getByText('结束时间必须晚于开始时间。',{exact:true});
  const inputBox=await to.boundingBox(),messageBox=await message.boundingBox(),queryBox=await query.boundingBox();
  expect(messageBox!.y).toBeGreaterThanOrEqual(inputBox!.y+inputBox!.height);
  expect(queryBox!.x>=messageBox!.x+messageBox!.width||queryBox!.y>=messageBox!.y+messageBox!.height).toBe(true);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('date-validation.png'),animations:'disabled'});
  await to.fill('2026-10-24T01:00');await expect(to).not.toHaveAttribute('aria-invalid','true');
  await expect(message).toHaveCount(0);
});
