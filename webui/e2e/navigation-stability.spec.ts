import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

// Headless Chromium normally hides the classic scrollbar that shifts Windows layouts.
test.use({launchOptions:{ignoreDefaultArgs:['--hide-scrollbars'],args:['--disable-features=OverlayScrollbar,FluentOverlayScrollbar']}});

for(const width of [1700,1280,800,390])test(`navigation and page origin stay aligned across routes at ${width}px`,async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width,height:900});await page.goto('/#/settings');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeVisible();
  const positions=[];
  for(const name of ['加速','历史','曲库','网络','设置','关于','加速']){
    await navigate(page,name);
    positions.push(await page.evaluate(name=>{
      const rect=(selector:string)=>{const r=[...document.querySelectorAll(selector)].find(el=>el.getClientRects().length)!.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width};};
      return {name,sidebar:rect('.navigation-sidebar'),content:rect('.page-content'),heading:rect('main h1'),header:rect('.shell > header'),scrollbar:innerWidth-document.documentElement.clientWidth};
    },name));
    if(name==='关于')await page.screenshot({path:info.outputPath('about.png'),animations:'disabled'});
  }
  await info.attach('route-positions',{body:JSON.stringify(positions,null,2),contentType:'application/json'});
  for(const position of positions){
    expect(position.sidebar).toEqual(positions[0].sidebar);
    expect(position.content).toEqual(positions[0].content);
    expect({x:position.heading.x,y:position.heading.y}).toEqual({x:positions[0].heading.x,y:positions[0].heading.y});
  }
  if(width>=1280){
    await navigate(page,'历史');await expect(page.getByText('当前筛选条件下暂无请求记录。',{exact:true})).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollHeight)).toBeLessThanOrEqual(900);
  }
});
