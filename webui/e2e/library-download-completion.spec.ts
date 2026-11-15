import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {expectWithClock} from './clock';
import {navigate} from './navigation';
import type {Download} from '../src/types';

for(const view of ['songs','resources'] as const)for(const scenario of ['first','page return','tab return','action refresh'] as const)test(`short external download before ${scenario} observation refreshes ${view}`,async({page})=>{
  const api=await mockAPI(page,{libraryView:'list'}),file=api.files[0];
  let saved=false,libraryReads=0,downloadReads=0,holdObservation=scenario==='first',releaseObservation=()=>{};
  const observation=new Promise<void>(resolve=>{releaseObservation=resolve;});
  await page.route('**/api/downloads',async route=>{
    downloadReads++;if(holdObservation)await observation;
    return route.fulfill({json:{revision:saved?2:0,tasks:[]}});
  });
  await page.route('**/api/library?*',route=>{
    libraryReads++;
    return route.fulfill({json:{storageID:'fixture',databaseID:'fixture-db',songs:[{id:file.songs[0].id,title:file.songs[0].title,artist:'',dancer:'',group:'',policy:'normal',score:1,hasDemand:true,resource:file.key}],keys:[file.key],total:1,fileCount:saved?1:0,totalBytes:saved?file.bytes:0,
      resources:{[file.key]:{key:file.key,size:file.bytes,duration:null,songs:file.songs,songCount:file.songCount,local:saved?file:null}}}});
  });
  await page.clock.install();await page.goto('/#/library?view='+view);
  const row=page.getByRole('table',{name:view==='songs'?'曲库歌曲':'曲库资源'}).locator('tbody tr').first();
  await expect(row).toContainText('未保存');await expect.poll(()=>downloadReads).toBe(1);
  if(scenario!=='first'){
    await expect.poll(()=>libraryReads).toBe(2);holdObservation=true;
    if(scenario==='page return'){
      await navigate(page,'关于');await page.goBack();
    }else if(scenario==='tab return'){
      for(const hidden of [true,false])await page.evaluate(value=>{Object.defineProperty(document,'hidden',{configurable:true,value});document.dispatchEvent(new Event('visibilitychange'));},hidden);
    }else await page.locator('#batch').getByRole('button',{name:'检查缺失视频',exact:true}).click();
    await expect.poll(()=>downloadReads).toBe(2);
  }
  const initialReads=scenario==='first'?1:3;
  await expect.poll(()=>libraryReads).toBe(initialReads);await expect(row).toContainText('未保存');
  saved=true; // The whole flight finishes after the library read, before the new download baseline.
  releaseObservation();
  await expectWithClock(page,()=>row.textContent()).toContain('已保存');expect(libraryReads).toBe(initialReads+1);
  await expect(row.getByRole('button',{name:'删除本地视频',exact:true})).toBeEnabled();
  await expect(page.getByRole('group',{name:'本地视频',exact:true})).toContainText('1 个 · 1.000 GiB');
  const reads=downloadReads;await page.clock.fastForward(31000);
  await expect.poll(()=>downloadReads).toBeGreaterThan(reads);expect(libraryReads).toBe(initialReads+1);
  expect(api.calls.some(call=>call.path==='inventory/scan')).toBe(false);
});

for(const view of ['songs','resources'] as const)test(`idle library observes external download start and completion in ${view}`,async({page})=>{
  const api=await mockAPI(page,{libraryView:'list'}),file=api.files[0];
  let saved=false,libraryReads=0,downloadReads=0,tasks:Download[]=[];
  await page.route('**/api/downloads',route=>{downloadReads++;return route.fulfill({json:{revision:saved?2:tasks.length?1:0,tasks}});});
  await page.route('**/api/library?*',route=>{
    libraryReads++;
    return route.fulfill({json:{storageID:'fixture',databaseID:'fixture-db',songs:[{id:file.songs[0].id,title:file.songs[0].title,artist:'',dancer:'',group:'',policy:'normal',score:1,hasDemand:true,resource:file.key}],keys:[file.key],total:1,fileCount:saved?1:0,totalBytes:saved?file.bytes:0,
      resources:{[file.key]:{key:file.key,size:file.bytes,duration:null,songs:file.songs,songCount:file.songCount,local:saved?file:null}}}});
  });
  await page.clock.install();await page.goto('/#/library?view='+view);
  const row=page.getByRole('table',{name:view==='songs'?'曲库歌曲':'曲库资源'}).locator('tbody tr').first();
  await expect(row).toContainText('未保存');await expect.poll(()=>downloadReads).toBe(1);
  await expect.poll(()=>libraryReads).toBe(2);
  await page.clock.fastForward(31000);expect(libraryReads).toBe(2);
  tasks=[{id:1,resource:file.key,host:'fixture',stage:'download',size:100,bytes:75,bytesPerSecond:10,idleMS:0}];
  await expectWithClock(page,()=>row.textContent()).toContain('下载中');await expect.poll(()=>libraryReads).toBe(3);
  saved=true;tasks=[];
  await expectWithClock(page,()=>row.textContent()).toContain('已保存');expect(libraryReads).toBe(4);
  await expect(page.getByRole('group',{name:'本地视频',exact:true})).toContainText('1 个 · 1.000 GiB');
  await page.clock.fastForward(31000);expect(libraryReads).toBe(4);
});

for(const scenario of ['between reads','after initial failure','after read failure','while tab hidden','off page','after resumed page failures','after resumed tab failures','after resumed action failures'] as const)test(`short external download refreshes idle library ${scenario}`,async({page})=>{
  const api=await mockAPI(page,{libraryView:'list'}),file=api.files[0];
  const persistentFailure=scenario.startsWith('after resumed');
  let saved=false,fail=scenario==='after initial failure'||persistentFailure,downloadReads=0,libraryReads=0;
  await page.route('**/api/downloads',route=>{
    downloadReads++;
    return fail?route.fulfill({status:503,body:'unavailable'}):route.fulfill({json:{revision:saved?2:0,tasks:[]}});
  });
  await page.route('**/api/library?*',route=>{
    libraryReads++;
    return route.fulfill({json:{storageID:'fixture',databaseID:'fixture-db',songs:[{id:file.songs[0].id,title:file.songs[0].title,artist:'',dancer:'',group:'',policy:'normal',score:1,hasDemand:true,resource:file.key}],keys:[file.key],total:1,fileCount:saved?1:0,totalBytes:saved?file.bytes:0,
      resources:{[file.key]:{key:file.key,size:file.bytes,duration:null,songs:file.songs,songCount:file.songCount,local:saved?file:null}}}});
  });
  await page.clock.install();await page.goto('/#/library');
  const row=page.getByRole('table',{name:'曲库歌曲'}).locator('tbody tr').first();
  await expect(row).toContainText('未保存');await expect.poll(()=>downloadReads).toBe(1);
  const error=page.getByRole('listitem').filter({hasText:'下载状态暂时无法读取。'});
  if(fail)await expect(error).toBeVisible();
  await expect.poll(()=>libraryReads).toBe(fail?1:2);
  if(persistentFailure){
    if(scenario==='after resumed page failures'){
      await navigate(page,'关于');await navigate(page,'曲库');
    }else if(scenario==='after resumed tab failures'){
      for(const hidden of [true,false])await page.evaluate(value=>{Object.defineProperty(document,'hidden',{configurable:true,value});document.dispatchEvent(new Event('visibilitychange'));},hidden);
    }else await page.locator('#batch').getByRole('button',{name:'检查缺失视频',exact:true}).click();
    await expect.poll(()=>libraryReads).toBe(2);
    const restoredReads=downloadReads;
    await expectWithClock(page,()=>downloadReads).toBeGreaterThan(restoredReads+1);
    await expect(error).toBeVisible();expect(libraryReads).toBe(2);
  }
  if(scenario==='after read failure'){
    fail=true;await page.clock.fastForward(5000);await expect(error).toBeVisible();
  }
  if(scenario==='off page')await navigate(page,'关于');
  if(scenario==='while tab hidden')await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});
  const before={downloadReads,libraryReads};
  saved=true;fail=false; // Both lifecycle edges happened without an active snapshot.
  if(scenario==='off page'||scenario==='while tab hidden'){
    await page.clock.fastForward(31000);expect({downloadReads,libraryReads}).toEqual(before);
    if(scenario==='off page')await navigate(page,'曲库');
    else await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});
  }
  await expectWithClock(page,()=>row.textContent()).toContain('已保存');
  await expect(error).toHaveCount(0);expect(libraryReads).toBe(before.libraryReads+(scenario==='off page'||scenario==='while tab hidden'?2:1));
  const settled=libraryReads;await page.clock.fastForward(31000);
  await expect.poll(()=>downloadReads).toBeGreaterThan(before.downloadReads);expect(libraryReads).toBe(settled);
  expect(api.calls.some(call=>call.path==='inventory/scan')).toBe(false);
});

for(const view of ['songs','resources'] as const)for(const scenario of ['shared','after batch stops','another download remains'] as const)test(`completed ${scenario} download refreshes ${view} and usage once`,async({page},info)=>{
  const api=await mockAPI(page,{libraryView:'list'}),file=api.files[0];
  api.state.batch.running=scenario==='after batch stops';
  let saved=false,libraryReads=0,downloadReads=0;
  const task:Download={id:1,resource:file.key,host:'fixture',stage:'download',size:100,bytes:75,bytesPerSecond:10,idleMS:0};
  let tasks=scenario==='another download remains'?[task,{...task,id:2,resource:'key2'}]:[task];
  await page.route('**/api/downloads',route=>{downloadReads++;return route.fulfill({json:{revision:saved?(tasks.length?3:4):2,tasks}});});
  await page.route('**/api/library?*',route=>{
    libraryReads++;
    return route.fulfill({json:{storageID:'fixture',databaseID:'fixture-db',songs:[{id:file.songs[0].id,title:file.songs[0].title,artist:'歌手',dancer:'舞者',group:'',policy:'normal',score:1,hasDemand:true,resource:file.key}],keys:[file.key],total:1,fileCount:saved?1:0,totalBytes:saved?file.bytes:0,
      resources:{[file.key]:{key:file.key,size:file.bytes,duration:null,songs:file.songs,songCount:file.songCount,local:saved?file:null}}}});
  });
  await page.clock.install();await page.goto('/#/library?view='+view);
  const table=page.getByRole('table',{name:view==='songs'?'曲库歌曲':'曲库资源'}),row=table.locator('tbody tr').first(),usage=page.getByRole('group',{name:'本地视频',exact:true});
  const status=row.getByRole('cell').nth(view==='songs'?6:3);
  await expect(status).toHaveText('下载中');await expect(row.getByRole('progressbar')).toHaveCount(0);await expect(usage).toContainText('0 个');
  await expect.poll(()=>libraryReads).toBe(2);
  const column=table.getByRole('columnheader',{name:'本地状态',exact:true}),columnWidth=(await column.boundingBox())!.width;
  await page.screenshot({path:info.outputPath('downloading.png'),animations:'disabled'});
  if(scenario==='after batch stops'){
    api.state.batch.running=false;await expectWithClock(page,()=>libraryReads).toBe(3);
  }
  const activeReads=libraryReads;
  await expectWithClock(page,()=>downloadReads).toBeGreaterThan(1);expect(libraryReads).toBe(activeReads);
  saved=true;tasks=tasks.filter(current=>current.id!==task.id);
  await expectWithClock(page,()=>row.textContent()).toContain('已保存');await expect(row).not.toContainText('未保存');
  await expect(status).toHaveText('已保存');expect(Math.abs((await column.boundingBox())!.width-columnWidth)).toBeLessThan(1);
  await expect(row.getByRole('button',{name:'定位文件',exact:true})).toBeEnabled();
  await expect(row.getByRole('button',{name:'删除本地视频',exact:true})).toBeEnabled();
  await expect(usage).toContainText('1 个 · 1.000 GiB');expect(libraryReads).toBe(activeReads+1);
  if(tasks.length){
    const reads=downloadReads;await expectWithClock(page,()=>downloadReads).toBeGreaterThan(reads);
    expect(libraryReads).toBe(activeReads+1);
    tasks=[];await expectWithClock(page,()=>libraryReads).toBe(activeReads+2);
  }
  const settled={libraryReads,downloadReads};await page.clock.fastForward(31000);
  await expect.poll(()=>downloadReads).toBeGreaterThan(settled.downloadReads);expect(libraryReads).toBe(settled.libraryReads);
});
