import {test,expect,type Page} from '@playwright/test';
import {mockAPI} from './api';
import type {LibraryPage,LibraryResource} from '../src/types';

test('preview is lazy, uses the selected entry, and releases its native source',async({page})=>{
  await libraryFixture(page);
  const preparations:unknown[]=[],media:string[]=[];
  await page.route('**/api/library/preview',async route=>{preparations.push(route.request().postDataJSON());await route.fulfill({json:{url:'/media/preview?fixture=1'}});});
  await page.route('**/media/preview?**',async route=>{media.push(route.request().url());await route.fulfill({status:502,body:'unavailable'});});
  await page.goto('/#/library');
  const songs=page.getByRole('table',{name:'曲库歌曲'});
  await expect(songs.getByRole('button',{name:'预览',exact:true})).toHaveCount(3);
  expect(preparations).toEqual([]);expect(media).toEqual([]);
  await songs.getByRole('button',{name:'预览',exact:true}).first().click();
  const player=page.getByLabel('曲库预览播放器');
  await expect(player).toHaveAttribute('src','/media/preview?fixture=1');
  expect(preparations[0]).toEqual({storageID:'fixture',songID:'1'});
  const old=await player.elementHandle();
  await page.keyboard.press('Escape');
  await expect(player).toHaveCount(0);
  expect(await old!.evaluate(v=>v.getAttribute('src'))).toBeNull();
  await page.getByRole('tab',{name:'按资源'}).click();
  await page.getByRole('table',{name:'曲库资源'}).getByRole('button',{name:'预览',exact:true}).last().click();
  await expect(player).toHaveAttribute('src','/media/preview?fixture=1');
  expect(preparations.at(-1)).toEqual({storageID:'fixture',resource:missing});
  const resourcePlayer=await player.elementHandle();
  await page.evaluate(()=>{location.hash='/history';});
  await expect(player).toHaveCount(0);
  expect(await resourcePlayer!.evaluate(v=>v.getAttribute('src'))).toBeNull();
});

for(const close of ['Escape','button','backdrop'])test(`preview restores focus after ${close} dismissal`,async({page})=>{
  await libraryFixture(page);
  await page.route('**/api/library/preview',route=>route.fulfill({json:{url:'/media/preview?focus'}}));
  await page.route('**/media/preview?**',route=>route.fulfill({status:502,body:'fixture'}));
  await page.goto('/#/library');
  for(const view of ['按歌曲','按资源']){
    await page.getByRole('tab',{name:view}).click();
    const trigger=page.getByRole('table',{name:view==='按歌曲'?'曲库歌曲':'曲库资源'}).getByRole('button',{name:'预览',exact:true}).last();
    await trigger.focus();await page.keyboard.press('Enter');
    const player=page.getByLabel('曲库预览播放器');
    await expect(player).toHaveAttribute('src','/media/preview?focus');
    const old=await player.elementHandle();
    if(close==='Escape')await page.keyboard.press('Escape');
    else if(close==='button')await page.getByRole('button',{name:'关闭预览',exact:true}).click();
    else await page.mouse.click(5,5);
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(trigger).toBeFocused();
    expect(await old!.evaluate(v=>v.getAttribute('src'))).toBeNull();
  }
});

test('an abandoned preparation cannot replace a later preview',async({page})=>{
  await libraryFixture(page);
  let finishFirst!:()=>void,firstRequested=false;
  const first=new Promise<void>(resolve=>{finishFirst=resolve;});
  await page.route('**/api/library/preview',async route=>{
    const id=route.request().postDataJSON().songID;
    if(id==='1'){firstRequested=true;await first;}
    await route.fulfill({json:{url:'/media/preview?song='+id}});
  });
  const requested:string[]=[];
  await page.route('**/media/preview?**',async route=>{requested.push(route.request().url());await route.fulfill({status:502,body:'unavailable'});});
  await page.goto('/#/library');
  const buttons=page.getByRole('table',{name:'曲库歌曲'}).getByRole('button',{name:'预览',exact:true});
  await buttons.first().click();await expect.poll(()=>firstRequested).toBe(true);
  await page.getByRole('button',{name:'关闭预览',exact:true}).click();
  await buttons.nth(1).click();
  await expect(page.getByLabel('曲库预览播放器')).toHaveAttribute('src','/media/preview?song=2');
  finishFirst();
  await expect.poll(()=>requested.length).toBeGreaterThan(0);
  expect(requested.every(url=>url.endsWith('song=2'))).toBe(true);
  await page.getByRole('button',{name:'关闭预览',exact:true}).click();
});

test('blocked or unknown songs do not prepare; preparation errors stay in preview',async({page})=>{
  const {songs}=await libraryFixture(page);
  songs[0].resource='';
  // The fixture only inferred normal; replace the whole response policy through its typed source.
  Object.assign(songs[1],{policy:'blocked'});
  await page.route('**/api/library/preview',route=>route.fulfill({status:400,json:{error:'当前资源不可用，请更新曲库。'}}));
  await page.goto('/#/library');
  const buttons=page.getByRole('table',{name:'曲库歌曲'}).getByRole('button',{name:'预览',exact:true});
  await expect(buttons.nth(0)).toBeDisabled();await expect(buttons.nth(1)).toBeDisabled();
  await buttons.nth(0).hover();
  await expect(page.getByRole('tooltip')).toHaveText('尚不知道对应资源，请先更新曲库。');
  await buttons.nth(1).focus();
  await expect(page.getByRole('tooltip')).toHaveText('歌曲已屏蔽，请先解除屏蔽。');
  await page.keyboard.press('Enter');
  await page.keyboard.press('Space');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await buttons.nth(2).click();
  await expect(page.getByRole('dialog')).toContainText('当前资源不可用，请更新曲库。');
  await expect(page.getByLabel('曲库预览播放器')).not.toHaveAttribute('src');
});

const key='a'.repeat(32), missing='b'.repeat(32);
async function libraryFixture(page:Page,videoError?:string) {
  const api=await mockAPI(page),queries:URLSearchParams[]=[];
  const local={...api.files[0],key,songs:[{id:'1',title:'共同视频一'},{id:'2',title:'共同视频二'}],songCount:2};
  const resources:Record<string,LibraryResource>={
    [key]:{key,size:12345,duration:null,songs:local.songs,songCount:2,local},
    [missing]:{key:missing,size:null,duration:null,songs:[{id:'3',title:'缺失视频'}],songCount:1,local:null},
  };
  const songs=[1,2,3].map(id=>({id:String(id),title:id<3?'共同视频'+id:'缺失视频',artist:'歌手资料',dancer:'舞者资料',group:'社团资料',score:123.456,hasDemand:true,policy:'normal' as const,resource:id<3?key:missing}));
  await page.route('**/api/library?**',async route=>{
    const p=new URL(route.request().url()).searchParams;queries.push(p);
    const chosen=songs.filter(s=>(!p.get('song')||s.id===p.get('song'))&&(!p.get('q')||s.title.includes(p.get('q')!)));
    const keys=Object.keys(resources).filter(k=>!p.get('resource')||k===p.get('resource'));
    const offset=Number(p.get('offset')||0);
    const data:LibraryPage={videoError,storageID:'fixture',databaseID:'fixture-db',songs:chosen.slice(offset,offset+50),keys:keys.slice(offset,offset+50),resources,total:p.get('view')==='resources'?keys.length:chosen.length,fileCount:1,totalBytes:12345};
    await route.fulfill({json:data});
  });
  if(videoError)for(const resource of Object.values(resources))resource.local=null;
  return {...api,resources,songs,queries};
}

test('song and resource views share state and deletion impact; targets use existing batch',async({page})=>{
  const {calls}=await libraryFixture(page);
  await page.goto('/#/library');
  const songs=page.getByRole('table',{name:'曲库歌曲'});
  await expect(songs.getByText('已保存',{exact:true})).toHaveCount(2);
  await expect(songs.getByText('ID 1 · 歌手资料 · 舞者资料 / 社团资料')).toBeVisible();
  await songs.getByRole('button',{name:'删除本地视频'}).first().click();
  await expect(page.getByRole('dialog')).toContainText('影响 2 首已知关联歌曲');
  await expect(page.getByRole('dialog')).toContainText('共同视频一');
  await expect(page.getByRole('dialog')).toContainText('共同视频二');
  await page.getByRole('button',{name:'取消',exact:true}).click();
  expect(calls.some(c=>c.path==='cache/delete')).toBe(false);
  await songs.getByRole('button',{name:'下载歌曲',exact:true}).last().click();
  await expect.poll(()=>calls.find(c=>c.path==='batch/download')?.body).toEqual({storageID:'fixture',songID:'3'});
  await page.getByRole('tab',{name:'按资源'}).click();
  const list=page.getByRole('table',{name:'曲库资源'});
  await expect(list.getByText('已保存',{exact:true})).toHaveCount(1);
  await expect(list.getByText('时长未知').last()).toBeVisible();
  await list.getByRole('button',{name:'下载资源',exact:true}).last().click();
  await expect.poll(()=>calls.filter(c=>c.path==='batch/download').at(-1)?.body).toEqual({storageID:'fixture',resource:missing});
});

for(const theme of ['light','dark'] as const)test(`demand scores use a hundred-point display and distinguish their source in ${theme}`,async({page},info)=>{
  const {songs}=await libraryFixture(page);
  const cases=[
    {score:0.123456,hasDemand:true,text:'12.35'},
    {score:1,hasDemand:true,text:'100.0'},
    {score:1.23456,hasDemand:true,text:'123.5'},
    {score:12.3456,hasDemand:true,text:'1,235'},
    {score:0,hasDemand:true,text:'0.00'},
    {score:0.5,hasDemand:false,text:'50.00'},
    {score:0.012345,hasDemand:false,text:'1.23'},
    {score:0.0000123,hasDemand:false,text:'0.00'},
  ];
  const template=songs[0];
  songs.splice(0,songs.length,...cases.map((c,i)=>({...template,...c,id:String(i+1),title:`分数示例 ${i+1}`})));
  await page.emulateMedia({colorScheme:theme});await page.setViewportSize({width:1280,height:900});
  await page.goto('/#/library');
  const table=page.getByRole('table',{name:'曲库歌曲'}),rows=table.getByRole('row');
  await expect(rows).toHaveCount(cases.length+1);
  for(const [i,c] of cases.entries()) {
    const cell=rows.nth(i+1).getByRole('cell').nth(1);
    await expect(cell).toHaveText(c.text);
    await expect(cell).toHaveAccessibleName(`${c.hasDemand?'真实分数':'初始分数'} ${c.text}`);
    const colors=await cell.locator('span').evaluate((el,real)=>{
      const style=getComputedStyle(el),probe=document.createElement('span');
      probe.style.color=`var(--colorNeutralForeground${real?'1':'3'})`;el.append(probe);
      const wanted=getComputedStyle(probe).color;probe.remove();return {actual:style.color,wanted};
    },c.hasDemand);
    expect(colors.actual).toBe(colors.wanted);
  }
  const color=(i:number)=>rows.nth(i).getByRole('cell').nth(1).locator('span').evaluate(el=>getComputedStyle(el).color);
  expect(await color(1)).not.toBe(await color(6));
  await table.getByRole('button',{name:'歌曲 6 的详情',exact:true}).click();
  await expect(page.getByText('分数来源',{exact:true})).toBeVisible();
  await expect(page.getByText('初始分数',{exact:true})).toBeVisible();
  await page.keyboard.press('Escape');
  await page.screenshot({path:info.outputPath('demand-scores.png'),fullPage:true});
});

test('library pagination stays bounded and applies the submitted search',async({page})=>{
  const {songs,queries}=await libraryFixture(page);
  for(let id=4;id<=121;id++)songs.push({...songs[2],id:String(id),title:'额外歌曲 '+id});
  await page.goto('/#/library');
  const list=page.getByRole('table',{name:'曲库歌曲'});
  await expect(list.getByRole('row')).toHaveCount(51);
  await page.getByRole('button',{name:'下一页曲库'}).click();
  await expect(page.getByText('共 121 首歌曲 · 第 2 页',{exact:true})).toBeVisible();
  await expect(list.getByRole('row')).toHaveCount(51);
  await page.getByRole('button',{name:'下一页曲库'}).click();
  await expect(list.getByRole('row')).toHaveCount(22);
  await page.getByLabel('搜索曲库').fill('额外歌曲 121');
  await page.getByRole('button',{name:'查找',exact:true}).click();
  await expect(list.getByRole('row')).toHaveCount(2);
  expect(queries.at(-1)?.get('q')).toBe('额外歌曲 121');
  expect(queries.at(-1)?.get('offset')).toBe('0');
});

test('protected file stays saved and disabled; favorite only changes policy',async({page})=>{
  const {resources,calls}=await libraryFixture(page);resources[key].local!.protected=true;
  await page.goto('/#/library');
  const songs=page.getByRole('table',{name:'曲库歌曲'});
  await expect(songs.getByText('受保护')).toHaveCount(2);
  await expect(songs.getByRole('button',{name:'删除本地视频'}).first()).toBeDisabled();
  await page.getByLabel('歌曲 1 的偏好').selectOption('favorite');
  await expect.poll(()=>calls.find(c=>c.path==='songs')?.body).toEqual({storageID:'fixture-db',id:'1',policy:'favorite'});
  expect(calls.some(c=>c.path==='batch/download'||c.path==='batch/start')).toBe(false);
});

test('unavailable videos leave song preferences usable and availability unknown',async({page})=>{
  const {calls}=await libraryFixture(page,'测试视频盘已断开');
  await page.goto('/#/library');
  await expect(page.getByRole('group').filter({hasText:'视频目录不可用，本地视频状态未知。歌曲资料仍可查看。'})).toBeVisible();
  const songs=page.getByRole('table',{name:'曲库歌曲'});
  await expect(songs.getByText('未知',{exact:true})).toHaveCount(3);
  await expect(songs.getByText('未保存',{exact:true})).toHaveCount(0);
  await expect(page.getByRole('region',{name:'曲库概览'}).getByText('—',{exact:true})).toHaveCount(2);
  await expect(songs.getByRole('button',{name:'下载歌曲',exact:true}).first()).toBeDisabled();
  await page.getByLabel('歌曲 1 的偏好').selectOption('favorite');
  await expect.poll(()=>calls.find(c=>c.path==='songs')?.body).toEqual({storageID:'fixture-db',id:'1',policy:'favorite'});
  expect(calls.some(c=>c.path==='cache/delete'||c.path==='batch/download')).toBe(false);
});

test('historical object links retain exact identity and explain missing objects',async({page})=>{
  const {queries}=await libraryFixture(page);
  await page.route('**/api/history?**',route=>route.fulfill({json:{storageID:'fixture',requests:[{id:1,at:Date.now(),completedAt:Date.now(),songID:'999',resource:'c'.repeat(32),method:'GET',elapsedMS:5,bytes:5,responseBytes:5,status:200,outcome:'completed',cache:'HIT'}],hasMore:false,nextCursor:''}}));
  await page.goto('/#/timeline');
  await page.getByRole('link',{name:'ID 999',exact:true}).click();
  await expect(page.getByText('当前条件下没有匹配项。')).toBeVisible();
  expect(queries.at(-1)?.get('song')).toBe('999');
  await page.goBack();
  await page.getByRole('link',{name:'c'.repeat(32),exact:true}).click();
  await expect(page.getByRole('tab',{name:'按资源'})).toHaveAttribute('aria-selected','true');
  await expect(page.getByText('当前条件下没有匹配项。')).toBeVisible();
  expect(queries.at(-1)?.get('resource')).toBe('c'.repeat(32));
});

for(const width of [320,390,1280])test(`library layout at ${width}px`,async({page},info)=>{
  await libraryFixture(page);await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:width===390?'dark':'light',forcedColors:width===320?'active':'none'});
  await page.goto('/#/library');await expect(page.getByRole('table',{name:'曲库歌曲'})).toBeVisible();
  await page.getByRole('tab',{name:'按资源'}).click();
  await expect(page.getByRole('table',{name:'曲库资源'})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('library.png'),fullPage:true});
});


test('idle library preserves selections and deletion confirmations without polling',async({page})=>{
  const {resources,calls,queries}=await libraryFixture(page);
  await page.clock.install();
  await page.goto('/#/library');
  await page.getByRole('tab',{name:'按资源'}).click();
  const checkbox=page.getByRole('checkbox',{name:/^选择 /});
  await checkbox.check();
  let count=queries.length;
  resources[key].local!.lastRequest=Date.now();
  await page.clock.fastForward(11000);
  expect(queries).toHaveLength(count);
  await expect(checkbox).toBeChecked();
  await page.getByRole('button',{name:'删除选中视频',exact:true}).click();
  await expect(page.getByRole('dialog')).toContainText('影响 2 首已知关联歌曲');
  count=queries.length;
  await page.clock.fastForward(11000);
  expect(queries).toHaveLength(count);
  await page.getByRole('button',{name:'确认删除',exact:true}).click();
  await expect.poll(()=>calls.find(c=>c.path==='cache/delete')?.body).toEqual({storageID:'fixture',entries:[{key,stamp:resources[key].local!.stamp}]});
});

for(const change of ['stamp','protected','missing','title'] as const)test('deletion confirmation rejects refreshed '+change,async({page})=>{
  const {resources,calls,queries,state}=await libraryFixture(page);
  state.import.running=true;
  await page.clock.install();
  await page.goto('/#/library');
  await page.getByRole('tab',{name:'按资源'}).click();
  await page.getByRole('checkbox',{name:/^选择 /}).check();
  await page.getByRole('button',{name:'删除选中视频',exact:true}).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  if(change==='stamp')resources[key].local!.stamp='replacement-file';
  if(change==='protected')resources[key].local!.protected=true;
  if(change==='missing')resources[key].local=null;
  if(change==='title')resources[key].local!.songs[0].title='更新后的歌曲';
  const count=queries.length;
  state.import.running=false;
  await page.clock.fastForward(5100);
  await expect.poll(()=>queries.length).toBeGreaterThan(count);
  await page.getByRole('button',{name:'确认删除',exact:true}).click();
  await expect(page.getByRole('region',{name:'文件操作结果',exact:true})).toBeFocused();
  await expect(page.getByText('文件或歌曲关联已变化，请刷新后重新选择并确认。',{exact:true})).toBeVisible();
  expect(calls.some(c=>c.path==='cache/delete')).toBe(false);
  if(change==='missing')await expect(page.getByRole('checkbox',{name:/^选择 /})).toHaveCount(0);
  else if(change!=='title')await expect(page.getByText('已选择 0 个文件',{exact:true})).toBeVisible();
});

test('changing the library view or query clears its selection',async({page})=>{
  await libraryFixture(page);
  await page.goto('/#/library');
  await page.getByRole('tab',{name:'按资源'}).click();
  const checkbox=page.getByRole('checkbox',{name:/^选择 /});
  await checkbox.check();
  await page.getByLabel('筛选',{exact:true}).selectOption('saved');
  await page.getByRole('button',{name:'查找',exact:true}).click();
  await expect(checkbox).not.toBeChecked();
  await checkbox.check();
  await page.getByRole('tab',{name:'按歌曲'}).click();
  await page.getByRole('tab',{name:'按资源'}).click();
  await expect(checkbox).not.toBeChecked();
});


test('compact rows, tooltip and operation feedback keep the table in place',async({page})=>{
  const {songs}=await libraryFixture(page);
  for(let id=4;id<=12;id++)songs.push({...songs[2],id:String(id),title:'额外歌曲 '+id});
  await page.setViewportSize({width:1440,height:1000});
  await page.goto('/#/library');
  const table=page.getByRole('table',{name:'曲库歌曲'}),rows=table.getByRole('row');
  await expect(rows).toHaveCount(13);
  const before=await table.boundingBox();
  expect((await rows.nth(1).boundingBox())!.height).toBeLessThanOrEqual(72);
  for(const [target,description] of [[table.getByRole('button',{name:'按需求分数排序',exact:true}),'随时间衰减'],[table.getByRole('columnheader',{name:'偏好',exact:true}).getByText('偏好',{exact:true}),'收藏仅作标记']] as const){
    await target.hover();await expect(page.getByRole('tooltip')).toContainText(description);
    await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toHaveCount(0);
    await page.mouse.move(0,0);await target.focus();
    await expect(page.getByRole('tooltip')).toContainText(description);
    await expect(target).toHaveAccessibleDescription(new RegExp(description));
    await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toHaveCount(0);
  }
  await expect(table.getByRole('button',{name:/需求分数说明|歌曲偏好说明/})).toHaveCount(0);
  await page.getByLabel('歌曲 1 的偏好').selectOption('favorite');
  await expect(page.getByRole('listitem',{name:'歌曲 1 已设为收藏。',exact:true})).toBeVisible();
  expect((await table.boundingBox())!.y).toBe(before!.y);
  await page.getByRole('button',{name:'关闭通知'}).click();
  expect((await table.boundingBox())!.y).toBe(before!.y);
});

test('record details link navigates without leaving a blocking surface',async({page})=>{
  await libraryFixture(page);await page.goto('/#/library');
  await page.getByRole('button',{name:'歌曲 1 的详情',exact:true}).click();
  await page.getByRole('link',{name:key,exact:true}).click();
  await expect(page.getByRole('table',{name:'曲库资源'})).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.getByRole('button',{name:'歌曲 1 的详情',exact:true})).toHaveCount(0);
});

test('import keeps its draft and uses a single confirmation dialog',async({page})=>{
  const {state}=await libraryFixture(page);state.settings.databasePath='C:\\New-Storage';
  await page.goto('/#/library');await page.getByRole('button',{name:'导入视频',exact:true}).click();
  await page.getByRole('textbox',{name:'外部目录',exact:true}).fill('C:\\Videos');
  await page.getByRole('button',{name:'下载视频',exact:true}).click();
  await page.getByRole('button',{name:'导入视频',exact:true}).click();
  await expect(page.getByRole('textbox',{name:'外部目录',exact:true})).toHaveValue('C:\\Videos');
  await page.getByRole('button',{name:'扫描并导入',exact:true}).click();
  await expect(page.getByRole('dialog')).toHaveCount(1);
  await expect(page.getByRole('dialog')).toContainText('新的数据位置尚未生效');
  await page.getByRole('button',{name:'取消',exact:true}).click();
  await expect(page.getByRole('textbox',{name:'外部目录',exact:true})).toBeVisible();
});

for(const width of [390,1280])for(const anchor of ['/library?view=resources','/library?view=songs','batch','vrcx-import','video-import'])test(`library anchor #${anchor} locates content and preserves the import draft at ${width}px`,async({page})=>{
  await libraryFixture(page);await page.setViewportSize({width,height:720});await page.goto('/#/library');
  await page.getByRole('button',{name:'导入视频',exact:true}).click();
  const draft=page.getByRole('textbox',{name:'外部目录',exact:true});await draft.fill('C:\\Fixture-Videos');
  await page.evaluate(anchor=>{location.hash=anchor;},anchor);
  const target=page.locator(anchor.startsWith('/library')?'#library-browser':'#'+anchor);
  await expect(target).toBeVisible();await expect(target).toBeFocused();await expect(target).toBeInViewport();
  await expect(page.getByRole('table',{name:anchor==='/library?view=resources'?'曲库资源':'曲库歌曲',exact:true})).toBeVisible();
  await expect(draft).toBeVisible();
  await page.getByRole('button',{name:'导入视频',exact:true}).click();await expect(draft).toHaveValue('C:\\Fixture-Videos');
});

test('a failed refresh during deletion confirmation prevents acting on the retained snapshot',async({page})=>{
  const api=await mockAPI(page);api.state.import.running=true;await page.clock.install();await page.goto('/#/library?view=resources');
  await page.getByRole('table',{name:'曲库资源'}).locator('tbody tr').first().getByRole('button',{name:'删除本地视频',exact:true}).click();
  await page.route('**/api/library?*',route=>route.fulfill({status:503,json:{error:'offline'}}));
  api.state.import.running=false;
  await page.clock.fastForward(5100);await expect(page.getByText('曲库读取失败，请刷新重试。',{exact:true})).toBeVisible();
  await page.getByRole('dialog').getByRole('button',{name:'确认删除',exact:true}).click();
  await expect(page.getByRole('region',{name:'文件操作结果',exact:true})).toBeFocused();
  await expect(page.getByText('文件或歌曲关联已变化，请刷新后重新选择并确认。',{exact:true})).toBeVisible();
  expect(api.calls.some(c=>c.path==='cache/delete')).toBe(false);
});

test('batch deletion focuses one persistent result with skipped and failed files',async({page},info)=>{
  const {resources}=await libraryFixture(page),template=resources[key];
  delete resources[key];delete resources[missing];
  for(let id=1;id<=50;id++){
    const key=id.toString(16).padStart(32,'0'),songs=id===50?[]:[{id:String(id),title:'批量视频 '+id}];
    resources[key]={...template,key,songs,songCount:songs.length,local:{...template.local!,key,songs,songCount:songs.length,stamp:'file-'+id}};
  }
  await page.route('**/api/cache/delete',async route=>{
    const entries=route.request().postDataJSON().entries as {key:string}[];
    const response=entries.map(({key},i)=>({key,result:i<46?'deleted':['protected','missing','changed','failed'][i-46]}));
    for(const entry of response)if(entry.result==='deleted'||entry.result==='missing')resources[entry.key].local=null;
    await route.fulfill({json:response});
  });
  await page.clock.install();await page.goto('/#/library?view=resources');
  await page.getByRole('button',{name:'选择本页可删除项',exact:true}).click();
  await page.getByRole('button',{name:'删除选中视频',exact:true}).click();
  await page.getByRole('dialog').getByRole('button',{name:'确认删除',exact:true}).click();
  const results=page.getByRole('region',{name:'文件操作结果',exact:true});
  await expect(results).toBeFocused();await expect(results).toBeInViewport();
  await expect(page.getByText('已删除 46 个，跳过 2 个，未删除 2 个',{exact:true})).toHaveCount(1);
  await page.clock.fastForward(8100);await expect(results).toBeVisible();
  await expect(results.getByRole('row')).toHaveCount(5);
  await expect(results).toContainText('批量视频 47');await expect(results).toContainText('文件或歌曲关联已变化');
  await expect(results).toContainText((50).toString(16).padStart(32,'0'));
  await expect(results).not.toContainText('批量视频 1');
  await page.setViewportSize({width:390,height:900});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await results.screenshot({path:info.outputPath('file-results.png'),animations:'disabled'});
});

test('library tabs name their result panel and catalog version supports hover and focus',async({page})=>{
  await libraryFixture(page);await page.goto('/#/library');
  const songs=page.getByRole('tab',{name:'按歌曲',exact:true});
  await songs.focus();await page.keyboard.press('ArrowRight');
  const resources=page.getByRole('tab',{name:'按资源',exact:true}),panel=page.getByRole('tabpanel',{name:'按资源',exact:true});
  await expect(resources).toBeFocused();await expect(songs).toHaveAttribute('aria-selected','true');
  await page.keyboard.press('Enter');await expect(panel.getByRole('table',{name:'曲库资源'})).toBeVisible();
  expect(await resources.getAttribute('aria-controls')).toBe(await panel.getAttribute('id'));
  const coverage=page.getByText('曲库覆盖率',{exact:true});
  await coverage.hover();await expect(page.getByRole('tooltip')).toHaveText('清单版本：未知');
  await page.keyboard.press('Escape');await page.mouse.move(0,0);
  await coverage.focus();await expect(page.getByRole('tooltip')).toHaveText('清单版本：未知');
  await expect(coverage).toHaveAccessibleDescription('清单版本：未知');
  await page.keyboard.press('Escape');await expect(page.getByRole('tooltip')).toHaveCount(0);
});
