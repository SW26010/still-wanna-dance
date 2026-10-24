import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('incoming GETs use the download snapshot, retain unknown identity and stop polling off page',async({page})=>{
  const {state}=await mockAPI(page);state.queue={running:true,desired:true,songs:[{songId:42}],titles:{42:'房间歌曲'},file:'test.log'};
  let reads=0;
  const requests=[{id:9,at:Date.now(),songID:'42',resource:'a'.repeat(32),status:206,bytes:50,responseBytes:100,range:'bytes=100-199'},{id:10,at:Date.now(),songID:'',resource:'',status:0,bytes:0,responseBytes:null,range:''}];
  await page.route('**/api/downloads',route=>{reads++;return route.fulfill({json:{tasks:[],bytesPerSecond:1000000,gets:{requests,total:requests.length}}});});
  await page.clock.install();await page.goto('/#/home');
  const list=page.getByRole('grid',{name:'当前 GET 请求列表'});
  await expect(list.getByText('请求歌曲：ID 42',{exact:true})).toBeVisible();
  await expect(list.getByText('请求歌曲：未知',{exact:true})).toBeVisible();
  await expect(list.getByText('已写出视频正文：50 B · 本次交付：50%',{exact:true})).toBeVisible();
  await expect(list.getByText('已写出视频正文：0 B · 本次交付：范围尚未确定',{exact:true})).toBeVisible();
  await expect(page.getByText('游戏日志：正在读取',{exact:true})).toBeVisible();
  await expect(page.getByText('房间歌曲',{exact:true})).toBeVisible();
  const first=reads;await page.clock.fastForward(5100);await expect.poll(()=>reads).toBe(first+1);
  await page.getByRole('button',{name:'当前请求 9 的范围',exact:true}).click();
  requests.splice(0,1);await page.clock.fastForward(5100);
  await expect(list.getByRole('row')).toHaveCount(1);await expect(page.locator('#current-gets')).toBeFocused();
  await page.getByRole('navigation',{name:'页面导航'}).getByRole('link',{name:'设置',exact:true}).click();
  const hidden=reads;await page.clock.fastForward(12000);expect(reads).toBe(hidden);
});

test('home shows actual gateway and unreadable log state at narrow width',async({page})=>{
  const {state}=await mockAPI(page);state.portOK=false;state.httpsPortOK=true;state.queue={desired:true,running:false,logError:'日志目录不可读取'};
  await page.setViewportSize({width:320,height:800});await page.emulateMedia({forcedColors:'active'});await page.goto('/#/home');
  await expect(page.getByText('HTTP 80：不可用',{exact:true})).toBeVisible();await expect(page.getByText('HTTPS 443：可用',{exact:true})).toBeVisible();
  await expect(page.getByText('游戏日志：读取失败',{exact:true})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
