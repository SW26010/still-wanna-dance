import {navigateSettings,navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

for(const width of [390,1280])test(`all navigation stays in one document and preserves drafts (${width})`,async({page})=>{
  const api=await mockAPI(page),requests:string[]=[],errors:string[]=[];
  await page.setViewportSize({width,height:900});
  page.on('request',request=>{if(['document','script','stylesheet'].includes(request.resourceType()))requests.push(request.url());});
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto('/#prepareAheadSeconds');
  const draft=page.getByRole('spinbutton',{name:/提前准备时间/});
  await draft.fill('777');
  const originalDocument=await page.evaluate(()=>performance.timeOrigin),initialRequests=requests.length;
  for(const name of ['加速','历史','曲库','网络','帮助','关于','使用条款','设置']){
    await navigate(page,name);
    await expect(page.getByText('正在读取程序状态…',{exact:true})).not.toBeVisible();
    if(width>640)await expect(page.locator('#main')).toBeFocused();
    expect(await page.evaluate(()=>performance.timeOrigin)).toBe(originalDocument);
  }
  await navigateSettings(page,'预加载与下载');await expect(draft).toHaveValue('777');
  await page.goBack();
  await expect(page.getByRole('tab',{name:'常规',exact:true})).toHaveAttribute('aria-selected','true');
  await page.goForward();
  await expect(draft).toHaveValue('777');
  expect(requests).toHaveLength(initialRequests);
  expect(api.calls.some(call=>call.path==='settings')).toBe(false);
  expect(errors).toEqual([]);
});

test('document body and footer links preserve navigation and drafts',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;
  await page.goto('/#prepareAheadSeconds');
  await page.getByRole('spinbutton',{name:/提前准备时间/}).fill('888');
  const originalDocument=await page.evaluate(()=>performance.timeOrigin);
  await navigate(page,'关于');
  await expect(page.getByRole('heading',{level:1})).toHaveText('关于');
  await page.getByRole('link',{name:'使用条款与内容权利声明',exact:true}).click();
  await expect(page.getByRole('button',{name:/恢复.*直连|停止播放网关/})).toHaveCount(0);
  await page.getByRole('link',{name:'返回首页',exact:true}).click();
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeEnabled();
  await navigate(page,'帮助');
  await page.getByRole('link',{name:'返回首页',exact:true}).click();
  await navigateSettings(page,'预加载与下载');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间/})).toHaveValue('888');
  expect(await page.evaluate(()=>performance.timeOrigin)).toBe(originalDocument);
});

for(const width of [390,1280])test(`library buttons locate persistent modules without leaving the list (${width})`,async({page})=>{
  const api=await mockAPI(page);await page.setViewportSize({width,height:900});
  await page.emulateMedia({colorScheme:width===390?'dark':'light'});
  await page.goto('/#/settings');
  const originalDocument=await page.evaluate(()=>performance.timeOrigin);
  for(let visit=0;visit<2;visit++){
    await navigate(page,'曲库');
    await expect(page.getByRole('button',{name:'查找',exact:true})).toBeEnabled();
    // Include the deferred focus work after the lazily mounted library renders.
    await page.evaluate(()=>new Promise<void>(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve()))));
    if(width>640)await expect(page.locator('#main')).toBeFocused();
    if(visit===0)await page.screenshot({path:`../artifacts/library-inline-modules/toolbar/top-${width}.png`,animations:'disabled'});
    for(const [name,id] of [['导入 VRCX 历史','vrcx-import'],['导入视频','video-import'],['下载视频','batch']]){
      const button=page.getByRole('button',{name,exact:true}),target=page.locator('#'+id);
      await button.scrollIntoViewIfNeeded();
      if(width>640){
        const tabs=(await page.getByRole('tablist',{name:'曲库视图',exact:true}).boundingBox())!,bounds=(await button.boundingBox())!;
        expect(bounds.x).toBeGreaterThan(tabs.x+tabs.width);
        expect(Math.abs(bounds.y+bounds.height/2-tabs.y-tabs.height/2)).toBeLessThan(2);
      }
      await button.focus();await button.press('Enter');
      await expect(target).toBeFocused();await expect(target.getByRole('heading',{level:2})).toBeInViewport();
      await expect(page.getByRole('table',{name:'曲库歌曲',exact:true})).toBeVisible();
      await expect(page).toHaveURL(/\/#\/library$/);
      if(visit===0)await page.screenshot({path:`../artifacts/library-inline-modules/toolbar/${id}-${width}.png`,animations:'disabled'});
    }
    const positions=await page.locator('#library-browser, #vrcx-import, #video-import, #batch').evaluateAll(elements=>elements.map(el=>el.getBoundingClientRect().top));
    expect(positions).toHaveLength(4);expect(positions).toEqual([...positions].sort((a,b)=>a-b));
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    expect(await page.evaluate(()=>performance.timeOrigin)).toBe(originalDocument);
    expect(api.calls.some(c=>/^(vrcx\/(preview|apply)|import\/start|batch\/(scan|start))$/.test(c.path))).toBe(false);
    await navigate(page,'设置');
  }
});

test('direct document URLs load content before status, normalize once, and survive refresh',async({page})=>{
  const api=await mockAPI(page);
  // A slow/unavailable status read must not turn static documents into loading pages.
  await page.route('**/api/status',route=>route.fulfill({status:503,body:'status unavailable'}));
  const documents:string[]=[];
  page.on('request',request=>{if(request.resourceType()==='document')documents.push(request.url());});
  for(const [path,title] of [['help','帮助'],['about','Load failed? But I still wanna dance!'],['terms','使用条款与内容权利声明']]){
    const count=documents.length;
    await page.goto('/'+path);
    await expect(page.getByRole('heading',{level:1,name:'关于',exact:true})).toBeVisible();
    await expect(page).toHaveURL(new RegExp('/#/('+path+')$'));
    await expect(page.getByText('正在读取程序状态…',{exact:true})).not.toBeVisible();
    if(path==='help')await expect(page.getByText('<script>alert("path")</script>',{exact:true})).toBeVisible();
    if(path==='about')await expect(page.getByText('dev-browser',{exact:true})).toBeVisible();
    if(path==='terms')await expect(page.getByRole('article',{name:'完整使用条款'})).toContainText('浏览器测试条款');
    expect(documents).toHaveLength(count+1);
    await page.reload();
    await expect(page.getByRole('heading',{level:1,name:'关于',exact:true})).toBeVisible();
    await expect(page.getByRole('heading',{level:2,name:title,exact:true})).toBeVisible();
  }
  expect(api.calls.some(call=>call.path==='setup')).toBe(false);
});

test('help shows active storage instead of a saved or unsaved storage change',async({page})=>{
  const api=await mockAPI(page);
  api.state.settings.databasePath='D:\\PendingStorage';
  await page.goto('/#prepareAheadSeconds');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间/})).toBeVisible();
  await navigate(page,'帮助');
  await expect(page.getByText(api.state.activeSettings.databasePath,{exact:true})).toBeVisible();
  await expect(page.getByText('D:\\PendingStorage',{exact:true})).not.toBeVisible();
});

test('public help/about stay readable before consent and terms still open the gate',async({page})=>{
  const api=await mockAPI(page);api.terms.accepted=false;
  for(const [path,title] of [['help','帮助'],['about','Load failed? But I still wanna dance!']]){
    await page.goto('/'+path+'?gate=1');
    await expect(page.getByRole('heading',{level:1,name:title,exact:true})).toBeVisible();
    await expect(page.getByRole('navigation').getByRole('link',{name:'设置',exact:true})).toHaveAttribute('href','/#/settings');
  }
  expect(api.calls).toHaveLength(0);
  for(const [name,target] of [['存储设置','storage-settings']]){
    await page.goto('/help?gate=1');
    const link=page.locator('#help-storage').getByRole('link',{name,exact:true});
    await expect(link).toHaveAttribute('href','/#'+target);
    await link.click();
    await expect(page).toHaveURL(new RegExp('/#'+target+'$'));
    await expect(page.getByRole('alertdialog',{name:'使用条款'})).toBeVisible();
    expect(api.calls.some(call=>call.path==='status')).toBe(false);
  }
  await page.goto('/terms?gate=1');
  await expect(page.getByRole('alertdialog',{name:'使用条款'})).toBeVisible();
  await expect(page.getByRole('button',{name:'同意完整条款',exact:true})).toBeVisible();
  expect(api.calls.some(call=>call.path==='status')).toBe(false);
});

for(const width of [390,1280])test(`ordinary navigation resets scrolling; explicit links locate fields (${width})`,async({page})=>{
  await mockAPI(page);
  await page.setViewportSize({width,height:720});
  await page.goto('/#prepareAheadSeconds');
  await page.getByRole('spinbutton',{name:/提前准备时间/}).fill('777');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间/})).toBeInViewport();
  for(const name of ['帮助','关于','加速']){
    await navigate(page,name);
    await expect.poll(()=>page.evaluate(()=>scrollY)).toBe(0);
    await page.evaluate(()=>scrollTo(0,document.body.scrollHeight));
  }
  await navigate(page,'帮助');
  await page.locator('#help-storage').getByRole('link',{name:'存储设置',exact:true}).click();
  const target=page.locator('#storage-settings');
  await expect(target).toBeFocused();
  await expect(target).toBeInViewport();
  await page.goBack();
  await expect.poll(()=>page.evaluate(()=>scrollY)).toBe(0);
  await page.goForward();
  await expect(target).toBeFocused();
  await navigateSettings(page,'预加载与下载');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间/})).toHaveValue('777');
});

test('action feedback names the operation and does not follow navigation or late responses',async({page})=>{
  await mockAPI(page);
  await page.goto('/#/library');
  await page.getByRole('button',{name:'检查缺失视频',exact:true}).click();
  await expect(page.getByRole('listitem',{name:'已开始检查缺失视频。',exact:true})).toBeVisible();
  await navigate(page,'设置');
  await expect(page.getByRole('listitem',{name:'已开始检查缺失视频。',exact:true})).not.toBeVisible();
  await navigate(page,'曲库');
  await expect(page.getByRole('listitem',{name:'已开始检查缺失视频。',exact:true})).not.toBeVisible();
  let finish!:()=>void;
  const pending=new Promise<void>(resolve=>{finish=resolve;});
  await page.route('**/api/batch/scan',async route=>{await pending;await route.fulfill({json:{ok:true}});});
  await page.getByRole('button',{name:'检查缺失视频',exact:true}).click();
  await expect(page.getByRole('listitem',{name:'正在处理…',exact:true})).toBeVisible();
  await navigate(page,'设置');
  finish();
  await expect(page.getByRole('listitem',{name:'已开始检查缺失视频。',exact:true})).not.toBeVisible();
  await navigate(page,'曲库');
  await expect(page.getByRole('listitem',{name:'已开始检查缺失视频。',exact:true})).not.toBeVisible();
});

for(const [document,outcome] of [['about','failed'],['help','unconfirmed'],['terms','accepted']] as const)test(`exit feedback remains visible on ${document} (${outcome})`,async({page})=>{
  await mockAPI(page);let release!:()=>void;
  const pending=new Promise<void>(resolve=>{release=resolve;});
  await page.route('**/api/exit',async route=>{
    await pending;
    if(outcome==='unconfirmed')await route.abort('connectionreset');
    else await route.fulfill(outcome==='failed'?{status:400,json:{error:'退出失败，请重试。'}}:{json:{ok:true,action:'exit'}});
  });
  await page.goto('/#/'+document);
  const exit=page.getByRole('button',{name:'退出程序',exact:true});
  await exit.click();await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.getByRole('listitem',{name:'正在处理…',exact:true})).toBeVisible();await expect(exit).toBeDisabled();
  release();
  const message=outcome==='failed'?'退出失败，请重试。':outcome==='unconfirmed'?/^操作结果尚未确认/:'正在退出应用并处理游戏连接。';
  await expect(page.getByRole('listitem',{name:message,exact:true})).toBeVisible();
  if(outcome==='accepted')await expect(exit).toBeDisabled();else await expect(exit).toBeEnabled();
  await expect(page.getByRole('heading',{level:1,name:'关于',exact:true})).toBeVisible();
});
