import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('incoming GETs use the download snapshot, retain unknown identity and stop polling off page',async({page})=>{
  const {state}=await mockAPI(page);state.queue={running:true,desired:true,songs:[{songId:42}],titles:{42:'房间歌曲'},file:'test.log'};
  let reads=0;
  const requests=[{id:9,at:Date.now(),songID:'42',resource:'a'.repeat(32),status:206,bytes:50,responseBytes:100,range:'bytes=100-199'},{id:10,at:Date.now(),songID:'',resource:'',status:0,bytes:0,responseBytes:null,range:''}];
  await page.route('**/api/downloads',route=>{reads++;return route.fulfill({json:{tasks:[],bytesPerSecond:1000000,gets:{requests,total:requests.length}}});});
  await page.clock.install();await page.goto('/#/home');
  const list=page.getByRole('table',{name:'当前 GET 请求列表'});
  await expect(list.getByRole('link',{name:'ID 42',exact:true})).toBeVisible();
  await expect(list.getByText('未知',{exact:true})).toBeVisible();
  await expect(list.getByText('50%',{exact:true})).toBeVisible();
  await expect(list.getByText('—',{exact:true})).toBeVisible();
  await expect(page.getByText('游戏日志：正在读取',{exact:true})).toBeVisible();
  await expect(page.getByText('房间歌曲',{exact:true})).toBeVisible();
  const first=reads;await page.clock.fastForward(5100);await expect.poll(()=>reads).toBe(first+1);
  await page.getByRole('button',{name:'当前请求 9 的范围',exact:true}).click();
  requests.splice(0,1);await page.clock.fastForward(5100);
  await expect(list.getByRole('row')).toHaveCount(2);await expect(page.locator('#current-gets')).toBeFocused();
  await page.getByRole('navigation',{name:'页面导航'}).getByRole('link',{name:'设置',exact:true}).click();
  const hidden=reads;await page.clock.fastForward(12000);expect(reads).toBe(hidden);
});

test('home shows actual gateway and unreadable log state at narrow width',async({page})=>{
  const {state}=await mockAPI(page);state.portOK=false;state.httpsPortOK=true;state.queue={desired:true,running:false,logError:'日志目录不可读取'};
  await page.setViewportSize({width:320,height:800});await page.emulateMedia({forcedColors:'active'});await page.goto('/#/home');
  await expect(page.getByText('视频服务：未运行',{exact:true})).toBeVisible();
  await expect(page.getByText('游戏日志：读取异常',{exact:true})).toBeVisible();
  await expect(page.locator('#queue').getByRole('link',{name:'检查日志设置',exact:true})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

for(const width of [390,1280])test(`populated home is readable at ${width}px`,async({page},info)=>{
  const {state}=await mockAPI(page);state.running=true;state.hosts.ready=true;state.queue={running:true,desired:true,songs:[{songId:42},{songId:43}],titles:{42:'夏日的风',43:'星河'},current:42,file:'test.log'};
  await page.route('**/api/downloads',route=>route.fulfill({json:{bytesPerSecond:1250000,gets:{total:1,requests:[{id:1,at:Date.now(),songID:'42',resource:'resource-42',status:206,bytes:50,responseBytes:100}]},tasks:[{id:'1',resource:'resource-42',stage:'download',songs:[{id:'42',title:'夏日的风'}],size:100000000,bytes:25000000,bytesPerSecond:1250000,idleMS:10}]}}));
  await page.setViewportSize({width,height:900});await page.goto('/');
  await expect(page.getByRole('heading',{name:'加速运行中',exact:true})).toBeVisible();
  await expect(page.getByRole('table',{name:'下载任务',exact:true}).getByText('夏日的风')).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  if(width>1000){const activity=await page.locator('.video-activity').boundingBox(),queue=await page.locator('#queue').boundingBox();expect(Math.abs(activity!.y-queue!.y)).toBeLessThan(2);}
  await page.screenshot({path:info.outputPath('home.png'),fullPage:true,animations:'disabled'});
});
