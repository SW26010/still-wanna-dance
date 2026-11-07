import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

const first='2026-10-30T12:00:00Z', later='2026-10-30T12:01:00Z';
const initial=[
  {operation:'catalog',entry:'api',state:'failed',reason:'timeout',observedAt:first},
  {operation:'catalog',entry:'kiva',state:'available',latencyMS:42,observedAt:later},
  {operation:'playback_url',entry:'cf',state:'available',latencyMS:71,observedAt:first},
  {operation:'resource',entry:'example.invalid',state:'failed',reason:'timeout',observedAt:first},
];

for(const selected of ['api','api-http','kiva','wanna'])test(`home keeps the selected catalog latency while other sources finish: ${selected}`,async({page},info)=>{
  const api=await mockAPI(page);await page.goto('/#/home');
  const badge=page.locator('.upstream-card').getByRole('button',{name:/检测歌曲列表/});
  const other=selected==='wanna'?'kiva':'wanna';
  const results=[
    {operation:'catalog',entry:selected,state:'available',selected:true,latencyMS:42,observedAt:first},
    {operation:'catalog',entry:other,state:'checking'},
  ];
  Object.assign(api.diagnostics.connectivity,{run:1,revision:2,running:true,operation:'catalog',phase:'running',results});
  await api.emit('connectivity');
  await expect(badge).toHaveText('歌曲列表：42 ms');
  await expect(badge).toHaveAccessibleName(`检测歌曲列表，${selected==='api'?'可连接':'主来源不可用，已使用备用清单'}，42 ms`);
  const expectedColor=await badge.evaluate((element,primary)=>{
    const sample=document.createElement('span');
    sample.style.backgroundColor=`var(--colorPalette${primary?'Green':'Yellow'}Background1)`;
    element.append(sample);const color=getComputedStyle(sample).backgroundColor;sample.remove();return color;
  },selected==='api');
  await expect(badge.locator('.fui-Badge')).toHaveCSS('background-color',expectedColor);
  const completed=[results[0],{operation:'catalog',entry:other,state:'failed',latencyMS:9000,observedAt:later}];
  Object.assign(api.diagnostics.connectivity,{revision:3,running:false,phase:'completed',results:completed,lastResult:{finished:later,results:completed}});
  await api.emit('connectivity');
  await expect(badge).toHaveText('歌曲列表：42 ms');
  await expect(badge.locator('.fui-Badge')).toHaveCSS('background-color',expectedColor);
  if(selected!=='api'){
    await badge.hover();
    await expect(page.getByRole('tooltip')).toContainText('主来源不可用，已使用备用清单');
    if(selected==='kiva'){
      const card=(await page.locator('.upstream-card').boundingBox())!,tip=(await page.getByRole('tooltip').boundingBox())!;
      const x=Math.min(card.x,tip.x)-8,y=Math.min(card.y,tip.y)-8;
      await page.screenshot({path:info.outputPath('catalog-fallback-warning.png'),animations:'disabled',clip:{x,y,width:Math.max(card.x+card.width,tip.x+tip.width)-x+8,height:Math.max(card.y+card.height,tip.y+tip.height)-y+8}});
    }
  }
  await navigate(page,'网络');
  await expect(page.locator('.diagnostic-rows')).toContainText(`${other==='wanna'?'WannaInfo':'Kiva'}：连接失败 · 响应时间 9000 ms`);
});

async function homeLayout(page:import('@playwright/test').Page) {
  return page.evaluate(()=>{
    const rect=(selector:string)=>{const r=document.querySelector(selector)!.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height};};
    return {upstream:rect('.upstream-card'),activation:rect('#activation'),demandsTop:rect('#block-demands').y};
  });
}

for(const width of [320,390,1280])test(`home cards fit as connectivity results and status change at ${width}px`,async({page},info)=>{
  await page.setViewportSize({width,height:900});
  const api=await mockAPI(page);await page.goto('/#/home');
  const card=page.locator('.upstream-card'),badge=card.getByRole('button',{name:/检测歌曲列表/});
  await expect(badge).not.toHaveAttribute('aria-disabled','true');
  const before=await homeLayout(page);
  const results=['catalog','playback_url','resource'].map(operation=>({operation,state:'available',entry:operation,latencyMS:30000,observedAt:first}));
  Object.assign(api.diagnostics.connectivity,{run:1,revision:2,phase:'completed',results,lastResult:{finished:first,results}});
  await api.emit('connectivity');
  await expect(badge).toHaveText('歌曲列表：30000 ms');
  const measured=await homeLayout(page);
  expect({x:measured.upstream.x,y:measured.upstream.y,width:measured.upstream.width}).toEqual({x:before.upstream.x,y:before.upstream.y,width:before.upstream.width});
  const overflow=await card.locator('.fui-Badge').evaluateAll(badges=>badges.map(item=>({scroll:item.scrollHeight,client:item.clientHeight})));
  for(const item of overflow)expect(item.scroll).toBeLessThanOrEqual(item.client+1);
  await card.screenshot({path:info.outputPath('upstream-observations.png'),animations:'disabled'});
  const updated=results.map(row=>({...row,latencyMS:30001}));
  Object.assign(api.diagnostics.connectivity,{revision:3,results:updated,lastResult:{finished:first,results:updated}});await api.emit('connectivity');
  await expect(badge).toHaveText('歌曲列表：30001 ms');
  expect(await homeLayout(page)).toEqual(measured);
  Object.assign(api.diagnostics.connectivity,{revision:4,closed:true});await api.emit('connectivity');
  await expect(badge).toContainText('待确认');
  await expect(card.getByText('应用正在关闭。',{exact:true})).toHaveCount(0);
  await expect(card.locator('.fui-MessageBar')).toHaveCount(0);
  expect((await homeLayout(page)).upstream.y).toEqual(before.upstream.y);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('a disconnected stream does not add a message to the home layout',async({page})=>{
  await page.setViewportSize({width:390,height:900});
  const api=await mockAPI(page);await page.goto('/#/home');
  const card=page.locator('.upstream-card'),badge=card.getByRole('button',{name:/检测歌曲列表/});
  await expect(badge).not.toHaveAttribute('aria-disabled','true');
  const before=await homeLayout(page);
  await page.request.post('/__fixture/disconnect',{data:{id:api.fixtureID,kind:'connectivity'}});
  await expect(badge).toHaveText('歌曲列表：待确认');
  await expect(card.locator('.fui-MessageBar')).toHaveCount(0);
  expect((await homeLayout(page)).upstream.y).toEqual(before.upstream.y);
});

test('an action failure remains visible if the state stream disconnects before it fails',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/home');
  const refresh=page.locator('.upstream-card').getByRole('button',{name:'重新检测上游',exact:true});
  await expect(refresh).toBeEnabled();
  let started!:()=>void,release!:()=>void;
  const received=new Promise<void>(resolve=>{started=resolve;}),hold=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/network/connectivity/start',async route=>{started();await hold;await route.fulfill({status:503,json:{error:'测试请求失败'}});});
  await refresh.click();await received;
  await page.request.post('/__fixture/disconnect',{data:{id:api.fixtureID,kind:'connectivity'}});
  await expect(page.locator('.upstream-card').getByRole('button',{name:/检测歌曲列表/})).toContainText('待确认');
  release();
  const notice=page.getByRole('listitem',{name:/操作未确认.*测试请求失败/});
  await expect(notice).toBeVisible();
  await expect(refresh).toBeEnabled({timeout:10000});
  await expect(notice).toBeVisible();
});

test('home shows the oldest observation time with an explanatory tooltip',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/home');
  const card=page.locator('.upstream-card'),footer=card.locator('.upstream-updated');
  await expect(footer).toHaveText('—');
  await footer.hover();await expect(page.getByRole('tooltip')).toHaveText('最早检测');
  const empty=(await footer.boundingBox())!,emptyTip=(await page.getByRole('tooltip').boundingBox())!;
  expect(emptyTip.x).toBeGreaterThanOrEqual(empty.x+empty.width-1);
  expect(emptyTip.x-(empty.x+empty.width)).toBeLessThan(24);
  await footer.focus();await expect(footer).toHaveAccessibleDescription('最早检测');
  await page.mouse.move(0,0);
  const old='2026-10-30T09:00:00Z',catalog='2026-10-30T13:00:00Z',playback='2026-10-30T12:30:00Z',resource='2026-10-30T12:00:00Z';
  const results=[{operation:'catalog',entry:'api',state:'failed',observedAt:old},{operation:'catalog',entry:'kiva',state:'available',latencyMS:42,observedAt:catalog},{operation:'playback_url',entry:'cf',state:'available',latencyMS:71,observedAt:playback},{operation:'resource',entry:'example.invalid',state:'failed',observedAt:resource}];
  Object.assign(api.diagnostics.connectivity,{run:1,revision:2,phase:'completed',results,lastResult:{finished:catalog,results}});
  await api.emit('connectivity');
  const format=(at:string)=>page.evaluate(value=>new Date(value).toLocaleString(),at);
  await expect(footer).toBeVisible();
  await expect(footer).toHaveText(await format(resource));
  const f=(await footer.boundingBox())!,c=(await card.boundingBox())!;
  expect(c.y+c.height-(f.y+f.height)).toBeGreaterThanOrEqual(0);
  expect(c.y+c.height-(f.y+f.height)).toBeLessThanOrEqual(12);
  const updated=results.map(row=>row.operation==='resource'?{...row,observedAt:catalog}:row);
  Object.assign(api.diagnostics.connectivity,{revision:3,results:updated,lastResult:{finished:catalog,results:updated}});await api.emit('connectivity');
  await expect(footer).toHaveText(await format(playback));
});

for(const width of [320,390,1280])test(`oldest observation tooltip is anchored to the time at ${width}px`,async({page},info)=>{
  await page.setViewportSize({width,height:900});
  const api=await mockAPI(page);
  const results=[{operation:'catalog',entry:'api',state:'available',latencyMS:42,observedAt:first}];
  Object.assign(api.diagnostics.connectivity,{revision:2,phase:'completed',results,lastResult:{finished:first,results}});
  await api.emit('connectivity');await page.goto('/#/home');
  const footer=page.locator('.upstream-updated'),time=footer.locator('time');
  await footer.hover();
  const tip=page.getByRole('tooltip');await expect(tip).toHaveText('最早检测');
  await page.locator('.upstream-card').screenshot({path:info.outputPath('oldest-tooltip.png'),animations:'disabled'});
  const target=(await time.boundingBox())!,box=(await tip.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(target.x+target.width-1);
  expect(box.x-(target.x+target.width)).toBeLessThan(24);
  expect(Math.abs((target.y+target.height/2)-(box.y+box.height/2))).toBeLessThan(16);
});

test('a skipped resource check identifies the retained observation and its time',async({page})=>{
  const api=await mockAPI(page);
  const results=[{operation:'resource',state:'skipped'},{operation:'resource',entry:'example.invalid',state:'available',latencyMS:64,observedAt:first,retained:true}];
  Object.assign(api.diagnostics.connectivity,{run:1,revision:2,phase:'completed',results,lastResult:{finished:later,results}});
  await api.emit('connectivity');await page.goto('/#/home');
  const badge=page.locator('.upstream-card').getByRole('button',{name:/检测视频下载/});
  await expect(badge).toHaveText('视频下载：64 ms');
  await expect(badge).toHaveAccessibleName(/本次未检测.*上次可连接/);
  const background=(element:Element)=>getComputedStyle(element).backgroundColor;
  const missing=page.locator('.upstream-card').getByRole('button',{name:/检测歌曲列表/});
  expect(await badge.locator('.fui-Badge').evaluate(background)).toEqual(await missing.locator('.fui-Badge').evaluate(background));
  await badge.hover();
  await expect(page.getByRole('tooltip')).toHaveText('本次未检测；上次检测：'+await page.evaluate(value=>new Date(value).toLocaleString(),first));
  await page.locator('.upstream-card').getByRole('link',{name:'网络上游：直连',exact:true}).click();
  const rows=page.locator('.diagnostic-rows p').filter({hasText:'视频下载'});
  await expect(rows.filter({hasText:'未检测（缺少有效样本）'})).toBeVisible();
  await expect(rows.filter({hasText:'example.invalid'})).toContainText('上次结果');
});

for(const [mode,backend,label] of [['direct','external-socks5','直连'],['proxy','external-socks5','SOCKS5'],['proxy','mihomo','Mihomo']] as const) {
  test(`home uses the active ${label} connectivity observations`,async({page})=>{
    const api=await mockAPI(page);
    Object.assign(api.state.activeSettings,{upstreamMode:mode,proxyBackend:backend});
    Object.assign(api.state.settings,{upstreamMode:mode==='direct'?'proxy':'direct'});
    Object.assign(api.diagnostics.connectivity,{run:1,revision:2,running:false,phase:'completed',results:initial,lastResult:{finished:later,results:initial}});
    await api.emit('connectivity');await page.goto('/#/home');
    const card=page.locator('.upstream-card');
    const heading=card.getByRole('heading',{name:'网络上游：'+label,exact:true});
    const title=heading.getByRole('link',{name:'网络上游：'+label,exact:true});
    await expect(heading).toBeVisible();
    await expect(heading.getByRole('link')).toHaveCount(1);
    await expect(title).toHaveAttribute('href','#/network');
    const catalog=card.getByRole('button',{name:/检测歌曲列表/});
    await expect(catalog).toHaveText('歌曲列表：42 ms');
    await expect(card.getByRole('button',{name:/检测播放地址/})).toHaveText('播放地址：71 ms');
    await expect(card.getByRole('button',{name:/检测视频下载/})).toHaveText('视频下载：—');
    await expect(card.getByRole('button',{name:/检测视频下载/})).toHaveAccessibleName('检测视频下载，连接失败');
    await catalog.hover();
    await expect(page.getByRole('tooltip')).toHaveText('最后检测：'+await page.evaluate(at=>new Date(at).toLocaleString(),later));
    await expect(catalog).toHaveAccessibleDescription(/最后检测：/);
    await page.keyboard.press('Escape');await page.mouse.move(0,0);await catalog.focus();
    await expect(page.getByRole('tooltip')).toContainText('最后检测：');
    expect(api.calls.filter(call=>call.path==='network/connectivity/start')).toHaveLength(0);
    if(mode==='direct'){
      for(const fraction of [0.05,0.58,0.95]){
        const box=(await title.boundingBox())!;
        await title.click({position:{x:box.width*fraction,y:box.height/2}});
        await expect(page).toHaveURL(/#\/network$/);
        if(fraction!==0.95)await navigate(page,'加速');
      }
    } else {await title.focus();await page.keyboard.press('Enter');}
    await expect(page).toHaveURL(/#\/network$/);
    await expect(page.getByRole('heading',{name:'网络',level:1,exact:true})).toBeVisible();
    await expect(page.getByRole('heading',{name:'连通性检测',exact:true})).toBeVisible();
    const kiva=page.locator('.diagnostic-rows p').filter({hasText:'歌曲列表 · Kiva'});
    await expect(kiva).toContainText('可连接 · 响应时间 42 ms');
    await expect(kiva.locator('time')).toHaveAttribute('datetime',later);
  });
}

test('each badge requests just its own check and shares updates with network',async({page})=>{
  const api=await mockAPI(page);
  Object.assign(api.diagnostics.connectivity,{run:1,revision:2,phase:'completed',results:initial,lastResult:{finished:later,results:initial}});
  await api.emit('connectivity');await page.goto('/#/home');
  const card=page.locator('.upstream-card');
  let results=initial;
  for(const [name,operation] of [['歌曲列表','catalog'],['播放地址','playback_url'],['视频下载','resource']] as const){
    const button=card.getByRole('button',{name:new RegExp('检测'+name)});
    await expect(button).not.toHaveAttribute('aria-disabled','true');
    await button.focus();await page.keyboard.press('Enter');
    await expect.poll(()=>api.calls.filter(call=>call.path==='network/connectivity/start').length).toBe(['catalog','playback_url','resource'].indexOf(operation)+1);
    expect(api.calls.filter(call=>call.path==='network/connectivity/start').at(-1)?.body).toEqual({operation});
    await expect(button).toHaveAttribute('aria-disabled','true');
    await expect(button).toHaveText(name+'：检测中');
    results=results.map(row=>row.operation===operation?{...row,state:'available',reason:'',latencyMS:99,observedAt:'2026-10-30T13:00:00Z'}:row);
    Object.assign(api.diagnostics.connectivity,{revision:api.diagnostics.connectivity.revision+1,running:false,phase:'completed',results,lastResult:{finished:'2026-10-30T13:00:00Z',results}});
    await api.emit('connectivity');
    await expect(button).not.toHaveAttribute('aria-disabled','true');
    await expect(button).toContainText('99 ms');
  }
  await navigate(page,'网络');
  const resource=page.locator('.diagnostic-rows p').filter({hasText:'视频下载 · example.invalid'});
  await expect(resource).toContainText('可连接 · 响应时间 99 ms');
  await expect(resource.locator('time')).toHaveAttribute('datetime','2026-10-30T13:00:00Z');
});

test('the refresh button starts the same full connectivity check as the network page',async({page})=>{
  await page.emulateMedia({reducedMotion:'no-preference'});
  const api=await mockAPI(page);await page.goto('/#/home');
  const card=page.locator('.upstream-card'),refresh=card.getByRole('button',{name:'重新检测上游',exact:true}),icon=refresh.locator('.refresh-icon');
  await expect(refresh).toBeEnabled();
  await refresh.hover();await expect(page.getByRole('tooltip')).toHaveText('重新检测上游');
  const before=await homeLayout(page);
  await refresh.focus();await page.keyboard.press('Enter');
  await expect.poll(()=>api.calls.filter(call=>call.path==='network/connectivity/start').length).toBe(1);
  expect(api.calls.find(call=>call.path==='network/connectivity/start')?.body).toEqual({});
  await expect(refresh).toHaveAttribute('aria-busy','true');
  await expect(refresh).toBeDisabled();
  await expect(refresh).toBeFocused();
  await expect(icon).toHaveCSS('animation-name','refresh-spin');
  await expect(icon).toHaveCSS('animation-duration','1.5s');
  expect(await homeLayout(page)).toEqual(before);
  await refresh.dispatchEvent('click');
  expect(api.calls.filter(call=>call.path==='network/connectivity/cancel')).toHaveLength(0);
  expect(api.calls.filter(call=>call.path==='network/connectivity/start')).toHaveLength(1);
  Object.assign(api.diagnostics.connectivity,{revision:api.diagnostics.connectivity.revision+1,running:false,phase:'completed'});await api.emit('connectivity');
  await expect(refresh).toBeEnabled();
  await expect(refresh).toHaveAttribute('aria-busy','false');
  await expect(icon).toHaveCSS('animation-name','none');
  expect(await homeLayout(page)).toEqual(before);
});

test('home and network show concurrent checks and independent completion',async({page})=>{
  const api=await mockAPI(page);
  const results=[{operation:'catalog',entry:'api',state:'checking'},{operation:'playback_url',entry:'cf',state:'checking'},{operation:'resource',entry:'play.udon.dance',state:'checking'}];
  Object.assign(api.diagnostics.connectivity,{run:1,revision:2,running:true,phase:'running',results});
  await api.emit('connectivity');await page.goto('/#/home');
  const card=page.locator('.upstream-card');
  for(const name of ['歌曲列表','播放地址','视频下载'])await expect(card.getByRole('button',{name:new RegExp('检测'+name)})).toHaveText(name+'：检测中');
  results[1]=Object.assign(results[1],{state:'available',latencyMS:55,observedAt:first});
  Object.assign(api.diagnostics.connectivity,{revision:3,results});await api.emit('connectivity');
  await expect(card.getByRole('button',{name:/检测播放地址/})).toHaveText('播放地址：55 ms');
  await expect(card.getByRole('button',{name:/检测歌曲列表/})).toHaveText('歌曲列表：检测中');
  await expect(card.getByRole('button',{name:/检测视频下载/})).toHaveText('视频下载：检测中');
  await navigate(page,'网络');
  const rows=page.locator('.diagnostic-rows');
  await expect(rows).toContainText('歌曲列表 · Udon：检测中');
  await expect(rows).toContainText('播放地址 · CF：可连接 · 响应时间 55 ms');
  await expect(rows).toContainText('视频下载 · play.udon.dance：检测中');
});

test('automatic checks spin the refresh icon and respect reduced motion',async({page})=>{
  await page.emulateMedia({reducedMotion:'no-preference'});
  const api=await mockAPI(page);await page.goto('/#/home');
  const refresh=page.locator('.upstream-card').getByRole('button',{name:'重新检测上游',exact:true}),icon=refresh.locator('.refresh-icon');
  await expect(refresh).toBeEnabled();
  const before=await homeLayout(page);
  Object.assign(api.diagnostics.connectivity,{run:1,revision:2,running:true,phase:'running',operation:undefined});await api.emit('connectivity');
  await expect(refresh).toHaveAttribute('aria-busy','true');
  await expect(refresh).toBeDisabled();
  await expect(icon).toHaveCSS('animation-name','refresh-spin');
  expect(await homeLayout(page)).toEqual(before);
  await page.emulateMedia({reducedMotion:'reduce'});
  await expect(icon).toHaveCSS('animation-name','none');
  await expect(refresh).toHaveAttribute('aria-busy','true');
  Object.assign(api.diagnostics.connectivity,{revision:3,running:false,phase:'completed'});await api.emit('connectivity');
  await expect(refresh).toBeEnabled();
  await expect(refresh).toHaveAttribute('aria-busy','false');
  expect(await homeLayout(page)).toEqual(before);
});

test('unknown, disconnected and canceled states do not claim a fresh result',async({page})=>{
  const api=await mockAPI(page);await page.goto('/#/home');
  const card=page.locator('.upstream-card'),button=card.getByRole('button',{name:/检测歌曲列表/});
  for(const label of ['歌曲列表','播放地址','视频下载']){
    const badge=card.getByRole('button',{name:new RegExp('检测'+label)});
    await expect(badge).toHaveText(label+'：—');
    await badge.hover();await expect(page.getByRole('tooltip')).toHaveText('尚未检测');
  }
  await expect(button).toHaveAccessibleName('检测歌曲列表，尚未检测');
  await page.mouse.move(0,0);
  await button.focus();await expect(page.getByRole('tooltip')).toHaveText('尚未检测');
  Object.assign(api.diagnostics.connectivity,{revision:2,phase:'canceled',results:[],lastResult:{finished:later,results:initial}});
  await api.emit('connectivity');await expect(button).toHaveText('歌曲列表：42 ms');
  await page.request.post('/__fixture/disconnect',{data:{id:api.fixtureID,kind:'connectivity'}});
  await expect(button).toContainText('待确认');await expect(button).toHaveAttribute('aria-disabled','true');
});

test('each hover tooltip is positioned beside its own badge',async({page})=>{
  await page.setViewportSize({width:1280,height:900});
  const api=await mockAPI(page);
  Object.assign(api.diagnostics.connectivity,{run:1,revision:2,phase:'completed',results:initial,lastResult:{finished:later,results:initial}});
  await api.emit('connectivity');await page.goto('/#/home');
  const card=page.locator('.upstream-card'), centers:{x:number;y:number}[]=[];
  for(const [label,at,prefix] of [['歌曲列表',later,''],['播放地址',first,''],['视频下载',first,'连接失败；']]){
    const badge=card.getByRole('button',{name:new RegExp('检测'+label)});
    await badge.hover();
    const tip=page.getByRole('tooltip');
    await expect(tip).toHaveText(prefix+'最后检测：'+await page.evaluate(value=>new Date(value).toLocaleString(),at));
    const b=(await badge.boundingBox())!,t=(await tip.boundingBox())!;
    expect(Math.abs((t.x+t.width/2)-(b.x+b.width/2))).toBeLessThan(3);
    centers.push({x:t.x+t.width/2,y:t.y+t.height/2});
    await page.mouse.move(0,0);await expect(tip).toBeHidden();
  }
  expect(centers[0].x).toBeLessThan(centers[1].x);
  expect(centers[1].x).toBeLessThan(centers[2].x);
});

test('the three badges sit naturally next to one another when there is room',async({page})=>{
  await page.setViewportSize({width:1280,height:900});
  await mockAPI(page);await page.goto('/#/home');
  const badges=page.locator('.upstream-badges .status-badge-button');
  await expect(badges).toHaveCount(3);
  const boxes=await badges.evaluateAll(items=>items.map(item=>{const r=item.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width};}));
  for(let i=1;i<boxes.length;i++){
    expect(boxes[i].y).toBeCloseTo(boxes[0].y,0);
    expect(boxes[i].x-(boxes[i-1].x+boxes[i-1].width)).toBeLessThanOrEqual(12);
  }
});

test('a focused tooltip does not block another badge on a narrow screen',async({page})=>{
  await page.setViewportSize({width:320,height:900});
  const api=await mockAPI(page);await page.goto('/#/home');
  const card=page.locator('.upstream-card');
  const firstBadge=card.getByRole('button',{name:/检测歌曲列表/}),lastBadge=card.getByRole('button',{name:/检测视频下载/});
  await expect(lastBadge).not.toHaveAttribute('aria-disabled','true');
  await firstBadge.focus();await expect(page.getByRole('tooltip')).toHaveText('尚未检测');
  await lastBadge.hover({timeout:3000});
  await lastBadge.click({timeout:3000});
  await expect.poll(()=>api.calls.filter(call=>call.path==='network/connectivity/start').at(-1)?.body).toEqual({operation:'resource'});
});

test('latency display retains accessible status even when a value is unavailable',async({page})=>{
  const api=await mockAPI(page);
  const results=initial.map(row=>row.operation==='resource'?{...row,latencyMS:350}:row.operation==='playback_url'?{...row,latencyMS:null}:row);
  Object.assign(api.diagnostics.connectivity,{revision:2,phase:'completed',results,lastResult:{finished:later,results}});
  await api.emit('connectivity');await page.goto('/#/home');
  const button=page.locator('.upstream-card').getByRole('button',{name:/检测视频下载/});
  await expect(button).toHaveText('视频下载：350 ms');
  await expect(button).toHaveAccessibleName('检测视频下载，连接失败，350 ms');
  const playback=page.locator('.upstream-card').getByRole('button',{name:/检测播放地址/});
  await expect(playback).toHaveText('播放地址：—');
  await expect(playback).toHaveAccessibleName('检测播放地址，可连接');
});

test('a busy check is reported without canceling the existing run',async({page})=>{
  const api=await mockAPI(page);
  Object.assign(api.diagnostics.connectivity,{run:1,revision:2,phase:'completed',results:initial,lastResult:{finished:later,results:initial}});
  await api.emit('connectivity');await page.goto('/#/home');
  await page.route('**/api/network/connectivity/start',route=>route.fulfill({status:400,json:{error:'连通性检测正在进行，请稍后重试'}}));
  const button=page.locator('.upstream-card').getByRole('button',{name:/检测歌曲列表/});
  await expect(button).not.toHaveAttribute('aria-disabled','true');
  const before=await homeLayout(page);
  await button.click();
  await expect(page.getByRole('listitem',{name:'连通性检测正在进行，请稍后重试',exact:true})).toBeVisible();
  await expect(page.locator('.upstream-card .fui-MessageBar')).toHaveCount(0);
  expect(await homeLayout(page)).toEqual(before);
  Object.assign(api.diagnostics.connectivity,{run:2,revision:3,running:true,phase:'running',operation:''});await api.emit('connectivity');
  await expect(button).toHaveAttribute('aria-disabled','true');
  await button.dispatchEvent('click');
  expect(api.calls.filter(call=>call.path==='network/connectivity/cancel')).toHaveLength(0);
});

for(const [width,height] of [[320,900],[390,900],[1280,900],[1440,900]])test(`home cards fit at ${width}px`,async({page},info)=>{
  await page.setViewportSize({width,height});
  await page.emulateMedia({colorScheme:width===390?'dark':'light',forcedColors:width===320?'active':'none'});
  const api=await mockAPI(page);
  if(width===320)Object.assign(api.state.activeSettings,{upstreamMode:'proxy',proxyBackend:'external-socks5'});
  await page.goto('/#/home');
  const upstream=page.locator('.upstream-card'),activation=page.locator('#activation');
  await expect(upstream.getByRole('button',{name:/检测歌曲列表/})).toBeVisible();
  const a=(await upstream.boundingBox())!,b=(await activation.boundingBox())!;
  if(width>=1280){expect(a.x+a.width).toBeLessThanOrEqual(b.x);expect(a.y).toBeCloseTo(b.y,0);expect(a.height).toBeCloseTo(b.height,0);}
  else if(width<=390){expect(a.y+a.height).toBeLessThanOrEqual(b.y);}
  const badgeStyle=(element:Element)=>{const style=getComputedStyle(element);return [style.backgroundColor,style.color,style.fontSize,style.borderRadius,element.getBoundingClientRect().height];};
  expect(await upstream.locator('.fui-Badge').first().evaluate(badgeStyle)).toEqual(await page.getByRole('status',{name:'游戏连接',exact:true}).evaluate(badgeStyle));
  const titleStyle=(element:Element)=>{const style=getComputedStyle(element);return [style.fontSize,style.fontWeight,style.color];};
  const heading=upstream.getByRole('heading');
  expect(await heading.evaluate(titleStyle)).toEqual(await page.locator('#block-demands h2').evaluate(titleStyle));
  const headingBox=(await heading.boundingBox())!,refreshBox=(await upstream.getByRole('button',{name:'重新检测上游'}).boundingBox())!;
  expect(refreshBox.x).toBeGreaterThan(headingBox.x+headingBox.width);
  expect(a.x+a.width-(refreshBox.x+refreshBox.width)).toBeLessThanOrEqual(16);
  for(const link of await heading.getByRole('link').all()){
    expect(await link.evaluate(titleStyle)).toEqual(await heading.evaluate(titleStyle));
    await link.hover();expect(await link.evaluate(titleStyle)).toEqual(await heading.evaluate(titleStyle));
  }
  await page.mouse.move(0,0);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('home-upstream.png'),fullPage:true,animations:'disabled'});
});
