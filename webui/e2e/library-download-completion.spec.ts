import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {expectWithClock} from './clock';
import type {Download} from '../src/types';

for(const scenario of ['shared','after batch stops','another download remains'] as const)test(`completed ${scenario} download refreshes saved files and usage once`,async({page})=>{
  const api=await mockAPI(page),file=api.files[0];
  api.state.batch.running=scenario==='after batch stops';
  let saved=false,libraryReads=0,downloadReads=0;
  const task:Download={id:1,resource:file.key,host:'fixture',stage:'download',size:100,bytes:75,bytesPerSecond:10,idleMS:0};
  let tasks=scenario==='another download remains'?[task,{...task,id:2,resource:'key2'}]:[task];
  await page.route('**/api/downloads',route=>{downloadReads++;return route.fulfill({json:{tasks}});});
  await page.route('**/api/library?*',route=>{
    libraryReads++;
    return route.fulfill({json:{storageID:'fixture',databaseID:'fixture-db',songs:[],keys:[file.key],total:1,fileCount:saved?1:0,totalBytes:saved?file.bytes:0,
      resources:{[file.key]:{key:file.key,size:file.bytes,duration:null,songs:file.songs,songCount:file.songCount,local:saved?file:null}}}});
  });
  await page.clock.install();await page.goto('/#/library?view=resources');
  const row=page.getByRole('table',{name:'曲库资源'}).locator('tbody tr').first(),usage=page.getByRole('group',{name:'本地视频',exact:true});
  await expect(row).toContainText('下载中 75.0%');await expect(row).toContainText('未保存');await expect(usage).toContainText('0 个');
  if(scenario==='after batch stops'){
    api.state.batch.running=false;await expectWithClock(page,()=>libraryReads).toBe(2);
  }
  const activeReads=libraryReads;
  await expectWithClock(page,()=>downloadReads).toBeGreaterThan(1);expect(libraryReads).toBe(activeReads);
  saved=true;tasks=tasks.filter(current=>current.id!==task.id);
  await expectWithClock(page,()=>row.textContent()).toContain('已保存');await expect(row).not.toContainText('未保存');
  await expect(row.getByRole('button',{name:'定位文件',exact:true})).toBeEnabled();
  await expect(row.getByRole('button',{name:'删除本地视频',exact:true})).toBeEnabled();
  await expect(usage).toContainText('1 个 · 1.000 GiB');expect(libraryReads).toBe(activeReads+1);
  if(tasks.length){
    const reads=downloadReads;await expectWithClock(page,()=>downloadReads).toBeGreaterThan(reads);
    expect(libraryReads).toBe(activeReads+1);
    tasks=[];await expectWithClock(page,()=>libraryReads).toBe(activeReads+2);
  }
  const settled={libraryReads,downloadReads};await page.clock.fastForward(31000);
  expect({libraryReads,downloadReads}).toEqual(settled);
});
