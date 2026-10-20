import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

for(const width of [390,1280])test(`all navigation stays in one document and preserves drafts (${width})`,async({page})=>{
  const api=await mockAPI(page),requests:string[]=[],errors:string[]=[];
  await page.setViewportSize({width,height:900});
  page.on('request',request=>{if(['document','script','stylesheet'].includes(request.resourceType()))requests.push(request.url());});
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto('/#/settings');
  const draft=page.getByRole('spinbutton',{name:/提前准备时间 M/});
  await draft.fill('777');
  const originalDocument=await page.evaluate(()=>performance.timeOrigin),initialRequests=requests.length;
  for(const name of ['开始使用','运行状态','本地视频','准备歌曲','使用帮助','关于','使用条款','设置']){
    await navigate(page,name);
    await expect(page.getByText('正在读取控制台状态',{exact:true})).not.toBeVisible();
    if(width>640)await expect(page.locator('#main')).toBeFocused();
    expect(await page.evaluate(()=>performance.timeOrigin)).toBe(originalDocument);
  }
  await expect(draft).toHaveValue('777');
  await page.goBack();
  await expect(page.getByRole('heading',{name:'使用条款与内容权利声明',exact:true})).toBeVisible();
  await page.goForward();
  await expect(draft).toHaveValue('777');
  expect(requests).toHaveLength(initialRequests);
  expect(api.calls.some(call=>call.path==='settings')).toBe(false);
  expect(errors).toEqual([]);
});

test('document body and footer links share navigation, and terms actions refresh console state',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;
  await page.goto('/#/settings');
  await page.getByRole('spinbutton',{name:/提前准备时间 M/}).fill('888');
  const originalDocument=await page.evaluate(()=>performance.timeOrigin);
  await page.locator('footer').getByRole('link',{name:'关于',exact:true}).click();
  await expect(page.getByRole('heading',{level:1})).toHaveText('Load failed? But I still wanna dance!');
  await page.getByRole('link',{name:'使用条款与内容权利声明',exact:true}).click();
  const calls=api.calls.length;
  await page.getByRole('button',{name:'停止播放网关',exact:true}).click();
  await expect(page.getByText('播放网关已停止。',{exact:true})).toBeVisible();
  await expect.poll(()=>api.calls.slice(calls).some(call=>call.path==='status')).toBe(true);
  await page.getByRole('link',{name:'返回控制台',exact:true}).click();
  await expect(page.getByRole('button',{name:'启动播放网关',exact:true})).toBeEnabled();
  await navigate(page,'使用帮助');
  await page.getByRole('link',{name:'返回控制台',exact:true}).click();
  await navigate(page,'设置');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间 M/})).toHaveValue('888');
  expect(await page.evaluate(()=>performance.timeOrigin)).toBe(originalDocument);
});

test('direct document URLs load content before status, normalize once, and survive refresh',async({page})=>{
  const api=await mockAPI(page);
  // A slow/unavailable status read must not turn static documents into loading pages.
  await page.route('**/api/status',route=>route.fulfill({status:503,body:'status unavailable'}));
  const documents:string[]=[];
  page.on('request',request=>{if(request.resourceType()==='document')documents.push(request.url());});
  for(const [path,title] of [['help','使用帮助'],['about','Load failed? But I still wanna dance!'],['terms','使用条款与内容权利声明']]){
    const count=documents.length;
    await page.goto('/'+path);
    await expect(page.getByRole('heading',{level:1,name:title,exact:true})).toBeVisible();
    await expect(page).toHaveURL(new RegExp('/#/('+path+')$'));
    await expect(page.getByText('正在读取控制台状态',{exact:true})).not.toBeVisible();
    if(path==='help')await expect(page.getByText('<script>alert("path")</script>',{exact:true})).toBeVisible();
    if(path==='about')await expect(page.getByText('dev-browser',{exact:true})).toBeVisible();
    if(path==='terms')await expect(page.getByRole('article',{name:'完整使用条款'})).toContainText('浏览器测试条款');
    expect(documents).toHaveLength(count+1);
    await page.reload();
    await expect(page.getByRole('heading',{level:1,name:title,exact:true})).toBeVisible();
  }
  expect(api.calls.some(call=>call.path==='setup')).toBe(false);
});

test('help shows active storage instead of a saved or unsaved storage change',async({page})=>{
  const api=await mockAPI(page);
  api.state.settings.storageDir='D:\\PendingStorage';
  await page.goto('/#/settings');
  await expect(page.getByRole('spinbutton',{name:/提前准备时间 M/})).toBeVisible();
  await navigate(page,'使用帮助');
  await expect(page.getByText(api.state.activeSettings.storageDir,{exact:true})).toBeVisible();
  await expect(page.getByText('D:\\PendingStorage',{exact:true})).not.toBeVisible();
});

test('public help/about stay readable before consent and terms still open the gate',async({page})=>{
  const api=await mockAPI(page);api.terms.accepted=false;
  for(const [path,title] of [['help','使用帮助'],['about','Load failed? But I still wanna dance!']]){
    await page.goto('/'+path+'?gate=1');
    await expect(page.getByRole('heading',{level:1,name:title,exact:true})).toBeVisible();
    await expect(page.getByRole('navigation').getByRole('link',{name:'设置',exact:true})).toHaveAttribute('href','/#/settings');
  }
  expect(api.calls).toHaveLength(0);
  await page.goto('/terms?gate=1');
  await expect(page.getByRole('dialog',{name:'完整条款'})).toBeVisible();
  await expect(page.getByRole('button',{name:'同意完整条款',exact:true})).toBeVisible();
  expect(api.calls.some(call=>call.path==='status')).toBe(false);
});
