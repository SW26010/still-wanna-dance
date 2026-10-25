import {navigateSettings,navigate,navigateStep} from './navigation';
import {test,expect,type Page} from '@playwright/test';
import {mockAPI} from './api';

const report=(id:string)=>({id,accepted:1,skipped:0,failed:0,items:[{index:1,status:'accepted',protocol:'ss'}]});
const reports=(page:Page)=>page.getByText(/本次接受 1 个/);
async function selectMihomo(page:Page) {
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
}
async function inspect(page:Page,text='fixture-node') {
  await page.getByRole('textbox',{name:'粘贴节点内容',exact:true}).fill(text);
  await page.getByRole('button',{name:'检查并导入到草稿',exact:true}).click();
}
async function applyUnchangedWizard(page:Page) {
  await navigate(page,'帮助');
  await page.getByRole('link',{name:'重新进入使用引导',exact:true}).click();
  await navigateStep(page,'4. 启用');
  await page.getByRole('button',{name:'确认并应用',exact:true}).click();
  await expect(page.getByRole('heading',{name:'视频加速',level:1,exact:true})).toBeVisible();
  await page.evaluate(()=>{location.hash='network-proxy';});
  await expect(page.getByRole('combobox',{name:'上游连接',exact:true})).toHaveValue('direct');
}

test('wizard reset releases the checked settings draft, ordinary navigation retains it',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/proxy/import',r=>r.fulfill({json:report('settings-draft')}));
  await page.goto('/#network-proxy');await selectMihomo(page);await inspect(page);
  await expect(reports(page)).toBeVisible();
  await navigate(page,'帮助');await navigateSettings(page,'网络');
  await page.getByRole('link',{name:'代理设置',exact:true}).click();
  await expect(reports(page)).toBeVisible();
  expect(api.calls.some(c=>c.path==='proxy/import/discard')).toBe(false);
  await applyUnchangedWizard(page);
  await expect.poll(()=>api.calls.filter(c=>c.path==='proxy/import/discard').map(c=>c.body.id)).toEqual(['settings-draft']);
  await expect(reports(page)).toHaveCount(0);
  expect(api.calls.find(c=>c.path==='setup/apply')?.body.proxyImportID).toBe('');
});

test('late old-draft cleanup cannot restore a replacement after wizard reset',async({page})=>{
  const api=await mockAPI(page);let imports=0,release:()=>void=()=>{};
  const held=new Promise<void>(done=>{release=done;});
  const discards:string[]=[];
  await page.route('**/api/proxy/import',r=>r.fulfill({json:report('draft-'+(++imports))}));
  await page.route('**/api/proxy/import/discard',async r=>{
    const id=r.request().postDataJSON().id;discards.push(id);
    if(id==='draft-1')await held;
    await r.fulfill({json:{ok:true}}).catch(()=>{});
  });
  try {
    await page.goto('/#network-proxy');await selectMihomo(page);await inspect(page);
    await expect(reports(page)).toBeVisible();await inspect(page,'replacement-node');
    await expect.poll(()=>discards).toEqual(['draft-1']);
    await applyUnchangedWizard(page);
    const completed=page.waitForResponse(response=>response.url().endsWith('/api/proxy/import/discard') && response.request().postDataJSON()?.id==='draft-1');
    release();await completed;
    await expect(page.getByRole('combobox',{name:'上游连接',exact:true})).toBeEnabled();
    await expect(reports(page)).toHaveCount(0);
    await expect.poll(()=>discards).toEqual(['draft-1','draft-2']);

    await page.getByRole('combobox',{name:'播放地址接口选择',exact:true}).selectOption('cf');
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await expect.poll(()=>api.calls.find(c=>c.path==='settings')?.body.proxyImportID).toBe('');
  } finally {release();}
});

test('wizard reset unlocks inputs and releases a late import even after identical text is re-entered',async({page})=>{
  const api=await mockAPI(page);let requested=false,release:()=>void=()=>{};
  const held=new Promise<void>(done=>{release=done;});
  await page.route('**/api/proxy/import',async r=>{requested=true;await held;await r.fulfill({json:report('late-draft')}).catch(()=>{});});
  try {
    await page.goto('/#network-proxy');await selectMihomo(page);await inspect(page);
    await expect.poll(()=>requested).toBe(true);
    await applyUnchangedWizard(page);
    await expect(page.getByRole('combobox',{name:'上游连接',exact:true})).toBeEnabled();
    await selectMihomo(page);
    await page.getByRole('textbox',{name:'粘贴节点内容',exact:true}).fill('fixture-node');
    release();
    await expect.poll(()=>api.calls.filter(c=>c.path==='proxy/import/discard').map(c=>c.body.id)).toEqual(['late-draft']);
    await expect(reports(page)).toHaveCount(0);
    await expect(page.getByRole('textbox',{name:'粘贴节点内容',exact:true})).toHaveValue('fixture-node');
    await expect(page.getByRole('button',{name:'检查并导入到草稿',exact:true})).toBeEnabled();
  } finally {release();}
});

test('failed settings save retains the draft for retry and success does not discard it',async({page})=>{
  const api=await mockAPI(page);let fail=true;
  await page.route('**/api/proxy/import',r=>r.fulfill({json:report('saved-draft')}));
  await page.route('**/api/settings',async r=>{
    if(fail)return r.fulfill({status:500,json:{error:'fixture save failed'}});
    // The real status response never exposes the transient import ID.
    const {proxyImportID,...settings}=r.request().postDataJSON();
    expect(proxyImportID).toBe('saved-draft');
    Object.assign(api.state.settings,settings);api.state.restartRequired=true;
    await r.fulfill({json:{ok:true}});
  });
  await page.goto('/#network-proxy');await selectMihomo(page);await inspect(page);
  await expect(reports(page)).toBeVisible();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByText(/fixture save failed/)).toBeVisible();
  await expect(reports(page)).toBeVisible();fail=false;
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(reports(page)).toHaveCount(0);
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(api.calls.some(c=>c.path==='proxy/import/discard')).toBe(false);
});
