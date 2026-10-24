import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

const record=(id:number)=>({id,at:Date.now()-id*1000,completedAt:Date.now()-id*1000+800,resource:'a'.repeat(32),songID:id===1?'42':'',method:'GET',range:'bytes=0-99',elapsedMS:800,bytes:id===2?0:50,responseBytes:id===2?null:100,status:id===2?502:206,cache:'MISS',outcome:id===2?'failed':'canceled',firstBodyNS:id===2?null:10000000,firstBodyBlocking:id===2?null:true,songs:[{id:'999',title:'当前关联不是真实请求身份'}]});

test('history pages beyond 500 without growing the DOM; filtering leaves latency window alone',async({page})=>{
  await mockAPI(page);const queries:URLSearchParams[]=[];let latencyReads=0;
  await page.route('**/api/history?*',r=>{const q=new URL(r.request().url()).searchParams;queries.push(q);const index=Number(q.get('cursor')||0);return r.fulfill({json:{storageID:'fixture',requests:Array.from({length:50},(_,i)=>record(index*50+i+1)),hasMore:index<12,nextCursor:String(index+1)}});});
  await page.route('**/api/latency',r=>{latencyReads++;return r.fulfill({json:{from:Date.now()-72*3600000,to:Date.now(),availableFrom:Date.now()-3600000,partial:true,blocking:{samples:20,p50MS:10,p95MS:80},ready:{samples:0,p50MS:null,p95MS:null}}});});
  await page.clock.install();await page.goto('/#recent');
  await expect(page.getByRole('heading',{level:1,name:'时间线'})).toBeVisible();
  await expect(page.getByText('P50 10 ms · P95 80 ms',{exact:true})).toBeVisible();
  await expect(page.getByText(/数据不足 72 小时/)).toBeVisible();
  await expect(page.getByText('请求歌曲：ID 42',{exact:true})).toBeVisible();
  await expect(page.getByText('当前关联不是真实请求身份',{exact:false})).toHaveCount(0);
  await expect(page.getByText('交付：— · 0 B',{exact:true})).toBeVisible();
  for(let n=2;n<=12;n++){
    await page.getByRole('button',{name:'下一页历史',exact:true}).click();
    await expect(page.getByText(`第 ${n} 页 · 50 条`,{exact:true})).toBeVisible();
    await expect(page.getByRole('button',{name:/^请求 \d+ 的明细$/})).toHaveCount(50);
  }
  await expect(page.getByLabel('历史查询结果',{exact:true})).toBeFocused();
  const reads=queries.length;await page.clock.fastForward(5100);expect(queries).toHaveLength(reads);
  await page.getByRole('button',{name:'上一页历史',exact:true}).click();await expect(page.getByText('第 11 页 · 50 条',{exact:true})).toBeVisible();
  await page.getByLabel('开始时间（含）',{exact:true}).fill('2026-10-21T01:00');await page.getByLabel('结束时间（不含）',{exact:true}).fill('2026-10-22T01:00');
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
  await expect(page.getByText('传输中止（原因未确定）',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'请求 1 的明细',exact:true}).click();
  await expect(page.getByText('首正文：10 ms · Blocking',{exact:true})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('timeline.png'),fullPage:true,animations:'disabled'});
});

test('timeline retains filters across navigation and rejects inverted date ranges',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/timeline');
  await page.getByLabel('开始时间（含）',{exact:true}).fill('2026-10-23T01:00');await page.getByLabel('结束时间（不含）',{exact:true}).fill('2026-10-22T01:00');
  const count=api.calls.filter(c=>c.path==='history').length;
  await page.getByRole('button',{name:'查询历史',exact:true}).click();await expect(page.getByText('结束时间必须晚于开始时间。',{exact:true})).toBeVisible();expect(api.calls.filter(c=>c.path==='history')).toHaveLength(count);
  await navigate(page,'设置');await navigate(page,'时间线');await expect(page.getByLabel('开始时间（含）',{exact:true})).toHaveValue('2026-10-23T01:00');
});
