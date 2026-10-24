import {test,expect,type Page} from '@playwright/test';
import {mockAPI} from './api';
import type {LibraryPage,LibraryResource} from '../src/types';

const key='a'.repeat(32), missing='b'.repeat(32);
async function libraryFixture(page:Page) {
  const api=await mockAPI(page),queries:URLSearchParams[]=[];
  const local={...api.files[0],key,songs:[{id:'1',title:'共同视频一',current:true},{id:'2',title:'共同视频二',current:true}],songCount:2};
  const resources:Record<string,LibraryResource>={
    [key]:{key,size:12345,duration:null,songs:local.songs,songCount:2,local},
    [missing]:{key:missing,size:null,duration:null,songs:[{id:'3',title:'缺失视频',current:true}],songCount:1,local:null},
  };
  const songs=[1,2,3].map(id=>({id:String(id),title:id<3?'共同视频'+id:'缺失视频',artist:'歌手资料',dancer:'舞者资料',group:'社团资料',score:123.456,policy:'normal' as const,resource:id<3?key:missing}));
  await page.route('**/api/library?**',async route=>{
    const p=new URL(route.request().url()).searchParams;queries.push(p);
    const chosen=songs.filter(s=>(!p.get('song')||s.id===p.get('song'))&&(!p.get('q')||s.title.includes(p.get('q')!)));
    const keys=Object.keys(resources).filter(k=>!p.get('resource')||k===p.get('resource'));
    const offset=Number(p.get('offset')||0);
    const data:LibraryPage={storageID:'fixture',songs:chosen.slice(offset,offset+50),keys:keys.slice(offset,offset+50),resources,total:p.get('view')==='resources'?keys.length:chosen.length,fileCount:1,totalBytes:12345};
    await route.fulfill({json:data});
  });
  return {...api,resources,songs,queries};
}

test('song and resource views share state and deletion impact; targets use existing batch',async({page})=>{
  const {calls}=await libraryFixture(page);
  await page.goto('/#/library');
  const songs=page.getByRole('grid',{name:'曲库歌曲'});
  await expect(songs.getByText('完整本地视频已保存',{exact:true})).toHaveCount(2);
  await expect(songs.getByText('歌手：歌手资料 · 舞者／社团：舞者资料／社团资料')).toHaveCount(3);
  await songs.getByRole('button',{name:'删除本地视频'}).first().click();
  await expect(page.getByRole('dialog')).toContainText('影响 2 首已知关联歌曲');
  await expect(page.getByRole('dialog')).toContainText('共同视频一');
  await expect(page.getByRole('dialog')).toContainText('共同视频二');
  await page.getByRole('button',{name:'取消',exact:true}).click();
  expect(calls.some(c=>c.path==='cache/delete')).toBe(false);
  await songs.getByRole('button',{name:'下载歌曲',exact:true}).last().click();
  await expect.poll(()=>calls.find(c=>c.path==='batch/download')?.body).toEqual({storageID:'fixture',songID:'3'});
  await page.getByRole('tab',{name:'按资源'}).click();
  const list=page.getByRole('grid',{name:'曲库资源'});
  await expect(list.getByText('完整本地视频已保存',{exact:true})).toHaveCount(1);
  await expect(list.getByText('大小：未知 · 时长：未知')).toBeVisible();
  await list.getByRole('button',{name:'下载资源',exact:true}).last().click();
  await expect.poll(()=>calls.filter(c=>c.path==='batch/download').at(-1)?.body).toEqual({storageID:'fixture',resource:missing});
});

test('library pagination stays bounded and applies the submitted search',async({page})=>{
  const {songs,queries}=await libraryFixture(page);
  for(let id=4;id<=121;id++)songs.push({...songs[2],id:String(id),title:'额外歌曲 '+id});
  await page.goto('/#/library');
  const list=page.getByRole('grid',{name:'曲库歌曲'});
  await expect(list.getByRole('row')).toHaveCount(50);
  await page.getByRole('button',{name:'下一页曲库'}).click();
  await expect(page.getByText('共 121 首歌曲 · 第 2 页',{exact:true})).toBeVisible();
  await expect(list.getByRole('row')).toHaveCount(50);
  await page.getByRole('button',{name:'下一页曲库'}).click();
  await expect(list.getByRole('row')).toHaveCount(21);
  await page.getByLabel('搜索曲库').fill('额外歌曲 121');
  await page.getByRole('button',{name:'查找',exact:true}).click();
  await expect(list.getByRole('row')).toHaveCount(1);
  expect(queries.at(-1)?.get('q')).toBe('额外歌曲 121');
  expect(queries.at(-1)?.get('offset')).toBe('0');
});

test('protected file stays saved and disabled; favorite only changes policy',async({page})=>{
  const {resources,calls}=await libraryFixture(page);resources[key].local!.protected=true;
  await page.goto('/#/library');
  const songs=page.getByRole('grid',{name:'曲库歌曲'});
  await expect(songs.getByText('受保护（正在使用／校验／待播）')).toHaveCount(2);
  await expect(songs.getByRole('button',{name:'删除本地视频'}).first()).toBeDisabled();
  await page.getByLabel('歌曲 1 的偏好').selectOption('favorite');
  await expect.poll(()=>calls.find(c=>c.path==='songs')?.body).toEqual({storageID:'fixture',id:'1',policy:'favorite'});
  expect(calls.some(c=>c.path==='batch/download'||c.path==='batch/start')).toBe(false);
});

test('historical object links retain exact identity and explain missing objects',async({page})=>{
  const {queries}=await libraryFixture(page);
  await page.route('**/api/history?**',route=>route.fulfill({json:{storageID:'fixture',requests:[{id:1,at:Date.now(),completedAt:Date.now(),songID:'999',resource:'c'.repeat(32),method:'GET',elapsedMS:5,bytes:5,responseBytes:5,status:200,outcome:'completed',cache:'HIT'}],hasMore:false,nextCursor:''}}));
  await page.goto('/#/timeline');
  await page.getByRole('link',{name:'ID 999',exact:true}).click();
  await expect(page.getByText('该对象不在当前曲库列表范围内；历史身份仍保留，未替换为其他对象。')).toBeVisible();
  expect(queries.at(-1)?.get('song')).toBe('999');
  await page.goBack();
  await page.getByRole('link',{name:'c'.repeat(32),exact:true}).click();
  await expect(page.getByRole('tab',{name:'按资源'})).toHaveAttribute('aria-selected','true');
  await expect(page.getByText('该对象不在当前曲库列表范围内；历史身份仍保留，未替换为其他对象。')).toBeVisible();
  expect(queries.at(-1)?.get('resource')).toBe('c'.repeat(32));
});

for(const width of [320,390,1280])test(`library layout at ${width}px`,async({page})=>{
  await libraryFixture(page);await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:width===390?'dark':'light',forcedColors:width===320?'active':'none'});
  await page.goto('/#/library');await expect(page.getByRole('grid',{name:'曲库歌曲'})).toBeVisible();
  await page.getByRole('tab',{name:'按资源'}).click();
  await expect(page.getByRole('grid',{name:'曲库资源'})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
