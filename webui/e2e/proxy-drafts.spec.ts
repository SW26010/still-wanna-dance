import {test,expect,type Page} from '@playwright/test';
import {mockAPI} from './api';

const report=(id:string)=>({id,nodes:1,groups:0,name:'Base64 配置',importedAt:'2026-11-04T08:00:00Z'});
const reports=(page:Page)=>page.getByRole('textbox',{name:'当前配置文件',exact:true});
async function selectMihomo(page:Page) {
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
}
async function inspect(page:Page,text='fixture-node') {
  await page.getByRole('combobox',{name:'配置来源',exact:true}).selectOption('base64');
  await page.getByRole('textbox',{name:'导入配置文件',exact:true}).fill(text);
  await page.getByRole('button',{name:'导入',exact:true}).click();
}

test('failed settings save retains the draft for retry and success does not discard it',async({page})=>{
  const api=await mockAPI(page);let fail=true;
  await page.route('**/api/proxy/import',r=>r.fulfill({json:report('saved-draft')}));
  await page.route('**/api/settings',async r=>{
    if(fail)return r.fulfill({status:500,json:{error:'fixture save failed'}});
    // The real status response never exposes the transient import ID.
    const {proxyImportID,...settings}=r.request().postDataJSON();
    expect(proxyImportID).toBe('saved-draft');
    Object.assign(api.state.settings,settings);api.state.proxyNodes={configuration:report('saved-draft')};api.state.restartRequired=true;
    await r.fulfill({json:{ok:true}});
  });
  await page.goto('/#network-proxy');await selectMihomo(page);await inspect(page);
  await expect(reports(page)).toHaveValue(/Base64 配置.*1 个节点/);
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByText(/fixture save failed/)).toBeVisible();
  await expect(reports(page)).toHaveValue(/Base64 配置.*1 个节点/);fail=false;
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(reports(page)).toHaveValue(/Base64 配置.*1 个节点/);
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(api.calls.some(c=>c.path==='proxy/import/discard')).toBe(false);
});

test('save automatically imports pending URL once and retries a failed save with the same draft',async({page})=>{
  const api=await mockAPI(page);let imports=0,fail=true;
  const posts:Record<string,unknown>[]=[];
  const imported={...report('automatic-draft'),name:'URL 配置'};
  await page.route('**/api/proxy/import',r=>{
    imports++;
    expect(r.request().headers()['x-swd-import-source']).toBe('url');
    return r.fulfill({json:imported});
  });
  await page.route('**/api/settings',async r=>{
    const body=r.request().postDataJSON();posts.push(body);
    expect(body.proxyImportID).toBe('automatic-draft');
    for(const name of ['proxyImportText','proxyImportSource','proxyImportName'])expect(body).not.toHaveProperty(name);
    if(fail)return r.fulfill({status:500,json:{error:'fixture write failed'}});
    const {proxyImportID,...settings}=body;
    Object.assign(api.state.settings,settings);api.state.proxyNodes={configuration:imported};api.state.restartRequired=true;
    await r.fulfill({json:{ok:true}});
  });
  await page.goto('/#network-proxy');await selectMihomo(page);
  await page.getByRole('combobox',{name:'配置来源',exact:true}).selectOption('url');
  const input=page.getByRole('textbox',{name:'导入配置文件',exact:true});
  await input.fill('https://example.invalid/config.yaml?private-token=fixture');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByText(/fixture write failed/)).toBeVisible();
  await expect(reports(page)).toHaveValue(/URL 配置.*1 个节点/);await expect(input).toHaveValue('');
  fail=false;await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(imports).toBe(1);expect(posts).toHaveLength(2);
});

test('failed automatic validation keeps the current configuration and does not submit settings',async({page})=>{
  const api=await mockAPI(page);
  api.state.proxyNodes={configuration:{name:'current.yaml',nodes:4,groups:2,importedAt:'2026-11-04T08:00:00Z'}};
  await page.route('**/api/proxy/import',r=>r.fulfill({status:400,json:{code:'settings_validation',error:'配置无效',fields:{proxyImportText:'配置无效'}}}));
  await page.goto('/#network-proxy');await selectMihomo(page);
  const current=reports(page);await expect(current).toHaveValue(/current.yaml.*4 个节点.*2 个代理组/);
  const before=await current.inputValue();
  await page.getByRole('combobox',{name:'配置来源',exact:true}).selectOption('url');
  const input=page.getByRole('textbox',{name:'导入配置文件',exact:true});
  await input.fill('https://example.invalid/bad.yaml');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(input).toHaveAttribute('aria-invalid','true');
  await expect(input).toHaveValue('https://example.invalid/bad.yaml');
  await expect(current).toHaveValue(before);
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
});

test('save can retry an explicit import failure and clears its old field error',async({page})=>{
  const api=await mockAPI(page);let attempts=0;
  const imported=report('retried-import');
  await page.route('**/api/proxy/import',r=>{
    if(++attempts===1)return r.fulfill({status:400,json:{code:'settings_validation',error:'暂时无法检查',fields:{proxyImportText:'暂时无法检查'}}});
    return r.fulfill({json:imported});
  });
  await page.route('**/api/settings',async r=>{
    const {proxyImportID,...settings}=r.request().postDataJSON();
    expect(proxyImportID).toBe('retried-import');
    Object.assign(api.state.settings,settings);api.state.proxyNodes={configuration:imported};api.state.restartRequired=true;
    await r.fulfill({json:{ok:true}});
  });
  await page.goto('/#network-proxy');await selectMihomo(page);await inspect(page);
  const input=page.getByRole('textbox',{name:'导入配置文件',exact:true});
  await expect(input).toHaveAttribute('aria-invalid','true');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  await expect(input).not.toHaveAttribute('aria-invalid','true');
  expect(attempts).toBe(2);
});

test('a newly selected empty file is validated instead of silently keeping the old configuration',async({page})=>{
  const api=await mockAPI(page);let imports=0;
  api.state.proxyNodes={configuration:{name:'current.yaml',nodes:4,groups:2}};
  await page.route('**/api/proxy/import',r=>{
    imports++;expect(r.request().postData()||'').toBe('');
    return r.fulfill({status:400,json:{code:'settings_validation',error:'配置内容为空',fields:{proxyImportText:'配置内容为空'}}});
  });
  await page.goto('/#network-proxy');await selectMihomo(page);
  const input=page.getByRole('textbox',{name:'导入配置文件',exact:true});
	const chooser=page.waitForEvent('filechooser');await input.focus();await page.keyboard.press('Enter');
	await (await chooser).setFiles({name:'empty.yaml',mimeType:'text/yaml',buffer:Buffer.alloc(0)});
  await expect(input).toHaveValue('empty.yaml');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(input).toHaveAttribute('aria-invalid','true');
  await expect(reports(page)).toHaveValue(/current.yaml.*4 个节点/);
  expect(imports).toBe(1);expect(api.calls.some(c=>c.path==='settings')).toBe(false);
});
