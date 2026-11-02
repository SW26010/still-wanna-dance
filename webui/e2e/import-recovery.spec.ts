import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

for(const width of [390,1280])test(`import pause, resume and discard preserve the batch at ${width}px`,async({page},info)=>{
  const api=await mockAPI(page);
  api.state.import={id:42,present:true,running:true,source:'D:\\External videos',videoDir:'D:\\Videos',total:5,checked:2,imported:2,phase:'正在校验并导入'};
  for(const action of ['pause','resume','discard'])await page.route(`**/api/import/${action}`,async route=>{
    const body=route.request().postDataJSON();api.calls.push({path:`import/${action}`,body});
    if(action==='pause')Object.assign(api.state.import,{running:false,paused:true,phase:'导入已暂停，等待继续'});
    if(action==='resume')Object.assign(api.state.import,{running:true,paused:false,phase:'正在校验并导入'});
    if(action==='discard')api.state.import={};
    await route.fulfill({json:{ok:true}});
  });
  await page.setViewportSize({width,height:900});
  await page.goto('/#video-import');
  const panel=page.locator('#video-import');
  await expect(panel.getByRole('textbox',{name:'外部目录',exact:true})).toBeDisabled();
  await panel.getByRole('button',{name:'暂停',exact:true}).click();
  await expect(panel.getByRole('button',{name:'继续导入',exact:true})).toBeEnabled();
  await panel.screenshot({path:info.outputPath('paused.png')});
  await page.reload();
  await expect(panel.getByRole('status')).toContainText('2/5');
  expect(api.calls.filter(c=>c.path==='import/resume')).toHaveLength(0);
  await panel.getByRole('button',{name:'继续导入',exact:true}).click();
  await expect(panel.getByRole('button',{name:'暂停',exact:true})).toBeVisible();
  await panel.getByRole('button',{name:'暂停',exact:true}).click();
  await panel.getByRole('button',{name:'放弃本次',exact:true}).click();
  await expect(page.getByRole('dialog')).toContainText('已导入的视频和源文件均保留');
  await page.getByRole('dialog').getByRole('button',{name:'取消',exact:true}).click();
  expect(api.calls.filter(c=>c.path==='import/discard')).toHaveLength(0);
  await panel.getByRole('button',{name:'放弃本次',exact:true}).click();
  await page.getByRole('dialog').getByRole('button',{name:'确认',exact:true}).click();
  await expect(panel.getByRole('button',{name:'扫描并导入',exact:true})).toBeVisible();
  expect(api.calls.filter(c=>/^import\/(pause|resume|discard)$/.test(c.path)).map(c=>[c.path,c.body.id])).toEqual([['import/pause',42],['import/resume',42],['import/pause',42],['import/discard',42]]);
  expect(api.calls.some(c=>c.path==='import/start'||c.path==='import/cleanup')).toBe(false);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('import from another library remains visible and cannot resume or clean',async({page})=>{
  const api=await mockAPI(page);
  api.state.import={id:42,present:true,paused:true,scopeMismatch:true,source:'D:\\Source',videoDir:'E:\\Old videos',phase:'导入已暂停，等待继续',error:'本次导入的数据位置与当前生效位置不一致'};
  await page.goto('/#video-import');
  const panel=page.locator('#video-import');
  await expect(panel.getByRole('button',{name:'继续导入',exact:true})).toBeDisabled();
  await expect(panel.getByRole('button',{name:'清除已导入的源文件',exact:true})).toHaveCount(0);
  await expect(panel.getByText(/本次导入到完整视频目录/)).toContainText('E:\\Old videos');
  await expect(panel.getByRole('button',{name:'放弃本次',exact:true})).toBeEnabled();
});

test('pause availability follows the pausing state, independent of displayed text',async({page})=>{
  const api=await mockAPI(page);
  api.state.import={id:42,present:true,running:true,phase:'暂停请求处理中',pausing:true};
  await page.goto('/#video-import');
  const panel=page.locator('#video-import');
  const pause=panel.getByRole('button',{name:'暂停',exact:true});
  await expect(pause).toBeDisabled();

  Object.assign(api.state.import,{pausing:false,phase:'正在暂停'});
  await page.reload();
  await expect(pause).toBeEnabled();

  Object.assign(api.state.import,{running:false,pausing:false,paused:true,phase:'导入已暂停，等待继续'});
  await page.reload();
  await expect(panel.getByRole('button',{name:'继续导入',exact:true})).toBeEnabled();
});

test('pause stays unavailable until the accepted request is reflected in status',async({page})=>{
  const api=await mockAPI(page);
  api.state.import={id:42,present:true,running:true,phase:'正在校验并导入'};
  await page.goto('/#video-import');
  const panel=page.locator('#video-import');
  const pause=panel.getByRole('button',{name:'暂停',exact:true});
  await expect(pause).toBeEnabled();

  let release!:()=>void, arrived!:()=>void, pauses=0;
  const hold=new Promise<void>(resolve=>{release=resolve;});
  const started=new Promise<void>(resolve=>{arrived=resolve;});
  await page.route('**/api/status',async route=>{
    arrived();
    await hold;
    try { await route.fulfill({json:api.state}); } catch { /* A stale read may be aborted. */ }
  });
  await page.route('**/api/import/pause',async route=>{
    pauses++;
    Object.assign(api.state.import,{pausing:true,phase:'正在暂停'});
    await route.fulfill({json:{ok:true}});
  });
  try {
    await pause.click();
    await started;
    await expect(pause).toBeDisabled();
    expect(pauses).toBe(1);
  } finally { release(); }
  await expect(panel.getByRole('status')).toContainText('正在暂停');
  await expect(pause).toBeDisabled();

  Object.assign(api.state.import,{running:false,pausing:false,paused:true,phase:'导入已暂停，等待继续'});
  await page.reload();
  await expect(panel.getByRole('button',{name:'继续导入',exact:true})).toBeEnabled();
});

test('a rejected pause becomes available again after status refresh',async({page})=>{
  const api=await mockAPI(page);
  api.state.import={id:42,present:true,running:true,phase:'正在校验并导入'};
  await page.route('**/api/import/pause',route=>route.fulfill({status:409,json:{error:'无法暂停，请重试'}}));
  await page.goto('/#video-import');
  const pause=page.locator('#video-import').getByRole('button',{name:'暂停',exact:true});
  await pause.click();
  await expect(page.getByText('无法暂停，请重试',{exact:true})).toBeVisible();
  await expect(pause).toBeEnabled();
});

for(const cleanup of [false,true])test(`pause can finish as ${cleanup?'interrupted cleanup':'completed import'}`,async({page})=>{
  const api=await mockAPI(page);
  api.state.import={id:42,present:true,running:true,total:1,checked:1,phase:cleanup?'正在清理已导入的源文件':'正在校验并导入'};
  await page.route('**/api/import/pause',async route=>{
    Object.assign(api.state.import,{pausing:true,phase:'正在暂停'});
    await route.fulfill({json:{ok:true}});
  });
  await page.goto('/#video-import');
  const panel=page.locator('#video-import');
  const pause=panel.getByRole('button',{name:'暂停',exact:true});
  await pause.click();
  await expect(panel.getByRole('status')).toContainText('正在暂停');
  await expect(pause).toBeDisabled();

  Object.assign(api.state.import,{running:false,pausing:false,paused:false,cleanupAvailable:cleanup,phase:cleanup?'清理已暂停，继续清理需要重新确认':'导入完成'});
  await page.reload();
  await expect(pause).toHaveCount(0);
  await expect(panel.getByRole('button',{name:'继续导入',exact:true})).toHaveCount(0);
  await expect(panel.getByRole('button',{name:'结束本次',exact:true})).toBeEnabled();
  if(cleanup){
    const clean=panel.getByRole('button',{name:'清除已导入的源文件',exact:true});
    await expect(clean).toBeDisabled();
    await panel.getByRole('checkbox',{name:/清除原位置本次成功导入/}).check();
    await expect(clean).toBeEnabled();
  }
});
