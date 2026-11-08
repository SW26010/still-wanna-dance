import {navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('persistent faults keep the layout stable and retain their cause across pages',async({page})=>{
  const api=await mockAPI(page);
  await page.clock.install();await page.goto('/#/home');
  const heading=page.getByText('后台程序：存储故障',{exact:true});
  await expect(heading).toHaveCount(0);
  const before=await page.locator('#activation').boundingBox();
  const cause='local storage unavailable: '+('long-storage-path-'.repeat(15))+' Access is denied. <script>unsafe()</script>';
  api.state.storageError=cause;
  await page.clock.fastForward(5100);await expect(heading).toBeVisible();
  expect((await page.locator('#activation').boundingBox())!.y).toBe(before!.y);
  const showCause=async()=>{
    await page.getByRole('button',{name:/后台程序：.*，查看详情/}).focus();await page.keyboard.press('Enter');
    await expect(page.getByText('首个错误：'+cause,{exact:true})).toBeVisible();
    await expect(page.getByText(/修复后重新打开应用/)).toBeVisible();
    await page.keyboard.press('Escape');
  };
  await showCause();
  await page.setViewportSize({width:320,height:800});
  for(const name of ['历史','曲库','设置']){
    await navigate(page,name);await expect(heading).toBeVisible();await showCause();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  }
  await page.route('**/api/status',r=>r.fulfill({status:503,body:'offline'}));
  await page.clock.fastForward(5100);
  await page.getByRole('button',{name:/后台程序：.*，查看详情/}).focus();await page.keyboard.press('Enter');
  await expect(page.getByText('无法读取程序状态，请检查 SWD 是否仍在运行。',{exact:true})).toBeVisible();
  await expect(page.getByText('首个错误：'+cause,{exact:true})).toBeVisible();
  await page.keyboard.press('Escape');
  await page.unroute('**/api/status');delete api.state.storageError;
  await page.clock.fastForward(5100);await expect(heading).toHaveCount(0);
});

for(const missing of ['existing','video'] as const)test('existing data requires storage confirmation: '+missing,async({page},info)=>{
  const api=await mockAPI(page);
  const reason=missing==='existing'?'尚未指定数据库和完整视频目录。默认位置发现已有数据库资料：C:/UI-Test/storage.sqlite。请在存储设置中确认数据库、完整视频目录和视频保留方式，保存后重启。':'尚未指定完整视频目录。完整视频目录已有资料，需要确认其数据库与保留策略：C:/UI-Test/videos。请在存储设置中确认数据库、完整视频目录和视频保留方式，保存后重启。';
  api.state.storageConfirmation=reason;
  api.state.settings.videoDir='';
  if(missing==='existing')api.state.settings.databasePath='';
  api.state.activeSettings={...api.state.settings};
  let libraryReads=0;
  await page.route('**/api/library?*',route=>{libraryReads++;return route.fulfill({json:{storageID:'fixture',databaseID:'fixture-db',videoError:'尚未指定完整视频目录',songs:[],resources:{},keys:[],total:0,fileCount:0,totalBytes:0}});});
  await page.setViewportSize({width:missing==='existing'?1280:320,height:900});await page.goto('/#/home');
  const badge=page.getByRole('status',{name:'后台程序',exact:true});
  await expect(badge).toHaveText('后台程序：待确认存储');
  await page.getByRole('button',{name:'后台程序：待确认存储，查看详情',exact:true}).focus();await page.keyboard.press('Enter');
  await expect(page.getByRole('group',{name:'后台状态',exact:true})).toContainText(reason);
  await page.getByRole('link',{name:'确认存储设置',exact:true}).click();
  await expect(page.getByRole('tab',{name:'存储',exact:true})).toHaveAttribute('aria-selected','true');
  await expect(page.getByRole('group',{name:'待确认存储',exact:true})).toContainText(reason);
  await page.screenshot({path:info.outputPath('storage-confirmation.png'),fullPage:true});
  await navigate(page,'曲库');
  await expect(page.getByRole('group',{name:'待确认存储',exact:true})).toContainText(reason);
  await expect(page.getByRole('group').filter({hasText:'视频目录不可用，本地视频状态未知。'})).toHaveCount(0);
  await expect(page.getByText('检查失败',{exact:true})).toHaveCount(0);
  await expect(page.getByText('曲库读取失败，请刷新重试。',{exact:true})).toHaveCount(0);
  if(missing==='existing') {
    await expect(page.getByRole('button',{name:'检查本地视频',exact:true})).toBeDisabled();
    await expect(page.getByRole('button',{name:'更新曲库',exact:true})).toBeDisabled();
    expect(libraryReads).toBe(0);
    expect(api.calls.some(c=>c.path==='inventory')).toBe(false);
  } else {
    await expect(page.getByRole('button',{name:'检查本地视频',exact:true})).toBeEnabled();
    expect(libraryReads).toBeGreaterThan(0);
  }
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  expect(api.calls.some(c=>c.path==='settings'||c.path==='cache/delete'||c.path==='storage/create-database')).toBe(false);
});

test('pending database selection does not hide a configured video directory failure',async({page})=>{
  const api=await mockAPI(page);
  api.state.storageConfirmation='尚未指定数据库。默认位置发现已有数据库资料：C:/UI-Test/storage.sqlite。请在存储设置中确认数据库、完整视频目录和视频保留方式，保存后重启。';
  api.state.storageError='完整视频目录不可用：测试视频盘已断开';
  api.state.settings.databasePath='';api.state.activeSettings.databasePath='';
  await page.route('**/api/library?*',route=>route.fulfill({json:{storageID:'fixture',databaseID:'fixture-db',videoError:'测试视频盘已断开',songs:[],resources:{},keys:[],total:0,fileCount:0,totalBytes:0}}));
  await page.goto('/#/library');
  await expect(page.getByRole('status',{name:'后台程序',exact:true})).toHaveText('后台程序：存储故障');
  await expect(page.getByRole('group',{name:'待确认存储',exact:true})).toBeVisible();
  await expect(page.getByRole('group').filter({hasText:'视频目录不可用，本地视频状态未知。'})).toContainText('测试视频盘已断开');
  await expect(page.getByText('检查失败',{exact:true})).toBeVisible();
});

for(const policy of [false,true])test('confirm storage fills missing locations through the existing save flow: '+policy,async({page})=>{
  const api=await mockAPI(page), originalDatabase=api.state.settings.databasePath;
  api.state.settings.databasePath='';api.state.settings.videoDir='';api.state.activeSettings={...api.state.settings};
  api.state.storageConfirmation='默认位置发现已有资料，请确认存储设置。';
  api.state.storageIssues={databasePath:{severity:'warning',message:'尚未确认已有数据库',value:''},videoDir:{severity:'warning',message:'尚未确认完整视频目录',value:''}};
  await page.route('**/api/storage/check',r=>r.fulfill({json:{confirmVideoPolicy:policy}}));
  await page.goto('/#storage-settings');
  await expect(page.getByRole('button',{name:'数据库文件：警告',exact:true})).toBeVisible();
  await page.getByRole('button',{name:'数据库文件：警告',exact:true}).focus();
  await expect(page.getByRole('tooltip')).toContainText('尚未确认已有数据库');
  await page.keyboard.press('Escape');
  const confirm=page.getByRole('group',{name:'待确认存储',exact:true}).getByRole('button',{name:'确认并保存',exact:true});
  await confirm.click();
  if(policy){
    const dialog=page.getByRole('dialog',{name:'确认此目录的视频保留方式',exact:true});
    await expect(dialog).toContainText('不限制视频空间');
    await dialog.getByRole('button',{name:'取消',exact:true}).click();
    expect(api.calls.some(c=>c.path==='settings')).toBe(false);
    expect(api.state.settings.databasePath).toBe('');
    await page.getByRole('radio',{name:'不保留',exact:true}).check();
    await confirm.click();
    await expect(dialog).toContainText('不保留普通视频');
    await dialog.getByRole('button',{name:'确认并保存',exact:true}).click();
  }
  await expect(page.getByRole('region',{name:'设置操作',exact:true}).getByRole('status')).toHaveText('待重启');
  expect(api.state.settings.databasePath).toBe(originalDatabase);
  expect(api.state.settings.videoDir).toBe(api.state.defaultVideoDir);
  expect(api.state.activeSettings.databasePath).toBe('');
  expect(api.calls.some(c=>c.path==='path/select')).toBe(false);
  expect(api.calls.some(c=>c.path==='storage/prepare-database')).toBe(true);
  const saved=api.calls.find(c=>c.path==='settings')!.body;
  if(policy)expect(saved.confirmVideoPolicy).toMatchObject({databasePath:originalDatabase,videoDir:api.state.defaultVideoDir,videoStorageMode:'none'});
  await expect(page.getByRole('button',{name:'数据库文件：警告',exact:true})).toHaveCount(0);
  await expect(page.getByRole('group',{name:'待确认存储',exact:true})).toContainText('存储设置已保存，请重启后应用');
  await expect(confirm).toHaveCount(0);
});

test('confirmation never saves a failed database preparation',async({page})=>{
  const api=await mockAPI(page);
  api.state.settings.databasePath='';api.state.activeSettings.databasePath='';
  api.state.storageConfirmation='默认位置发现已有数据库资料，请确认。';
  await page.route('**/api/storage/prepare-database',r=>r.fulfill({status:400,body:'数据库格式损坏，未修改原文件'}));
  await page.goto('/#storage-settings');
  await page.getByRole('button',{name:'确认并保存',exact:true}).click();
  await page.getByRole('button',{name:'数据库文件：错误',exact:true}).focus();
  await expect(page.getByRole('tooltip')).toContainText('数据库格式损坏');
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
});

test('runtime storage failures use field validation and follow the affected path',async({page},info)=>{
  const api=await mockAPI(page);
  const original=api.state.settings.databasePath, cause='已配置的数据库文件不存在';
  api.state.storageError=cause;
  api.state.storageIssues={databasePath:{severity:'error',message:cause,value:original},blockCacheDir:{severity:'error',message:'缓存磁盘不可用',value:''}};
  await page.setViewportSize({width:320,height:900});
  await page.goto('/#storage-settings');
  const icon=page.getByRole('button',{name:'数据库文件：错误',exact:true});
  await icon.focus();await expect(page.getByRole('tooltip')).toContainText(cause);await page.keyboard.press('Escape');
  await page.screenshot({path:info.outputPath('storage-error-field.png'),fullPage:true});
  await page.getByRole('radio',{name:'不保留',exact:true}).check();
  await expect(icon).toBeVisible();
  await page.route('**/api/path/select',r=>r.fulfill({json:{path:'D:\\Repaired\\storage.sqlite',canceled:false}}));
  await page.getByRole('textbox',{name:'数据库文件',exact:true}).click();
  await expect(icon).toHaveCount(0);
  await page.getByRole('button',{name:'放弃更改',exact:true}).click();
  await expect(icon).toBeVisible();
  await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
  await page.getByRole('button',{name:'临时视频缓存目录：错误',exact:true}).focus();
  await expect(page.getByRole('tooltip')).toContainText('缓存磁盘不可用');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
