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
