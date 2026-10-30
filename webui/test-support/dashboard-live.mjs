import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
const [base,output,warning]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({locale:'zh-CN'}),errors=[],external=[],websockets=[];
  const managedGroup=page.locator('[data-group-name="SWD"]').first();
  const groupContent=()=>managedGroup.locator('.collapse-motion-content, .proxy-group-card-content');
  const choose=async name=>{
    if(!await groupContent().isVisible())await managedGroup.getByText('SWD',{exact:true}).click();
    await groupContent().getByText(name,{exact:true}).click();
    await expect(groupContent().getByText(name,{exact:true})).toHaveClass(/text-primary-content/);
  };
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(new URL(r.url()).origin!==base)external.push(r.url().split('?')[0]);});
  page.on('websocket',ws=>websockets.push(new URL(ws.url()).pathname));
  // Upstream unconditionally checks its own release; it is unrelated to the
  // embedded node controls. Exercise those controls with the Internet offline.
  await page.route('https://**/*',route=>route.abort('internetdisconnected'));
  await page.goto(base+'/#/settings');
  const token=await page.locator('meta[name="swd-token"]').getAttribute('content');
  await page.getByRole('tab',{name:'网络',exact:true}).click();
  if(warning)await expect(page.getByText(warning,{exact:true})).toBeVisible();
  await expect(page.getByRole('link',{name:'打开代理面板',exact:true})).toHaveAttribute('href','/proxy/');
  await expect(page.getByText('面板整组测速可能并发连接组内所有节点，节点较多时可能产生连接高峰。')).toBeVisible();
  await page.goto(base+'/proxy/');
  const group=async()=>{const r=await page.request.get(base+'/api/mihomo/proxies/SWD',{headers:{Authorization:'Bearer '+token}});expect(r.ok()).toBe(true);return r.json();};
  await expect.poll(()=>page.evaluate(()=>localStorage.getItem('config/automatic-disconnection'))).toBe('false');
  await expect(page.getByText('SWD',{exact:true}).first()).toBeVisible();
  const initial=await group(),node=initial.all[2];
  const backendID=await page.evaluate(()=>{
    const list=JSON.parse(localStorage.getItem('setup/api-list'));
    if(list.length!==1)throw Error('unexpected initial backends');
    list[0].password='expired-process-token';
    localStorage.setItem('setup/api-list',JSON.stringify(list));
    return list[0].uuid;
  });
  await page.goto(base+'/proxy/');
  await expect(page.getByText('SWD',{exact:true}).first()).toBeVisible();
  const backends=await page.evaluate(()=>JSON.parse(localStorage.getItem('setup/api-list')));
  expect(backends).toHaveLength(1);expect(backends[0].uuid).toBe(backendID);expect(backends[0].password).toBe(token);
  await page.screenshot({path:join(output,'dashboard-before-selection.png'),fullPage:true});
  await writeFile(join(output,'dashboard-body.txt'),await page.locator('body').innerText());
  await choose(node);
  await expect.poll(async()=>(await group()).now).toBe(node);
  await page.reload();
  await expect(page.getByText('SWD',{exact:true}).first()).toBeVisible();
  expect((await group()).now).toBe(node);
  await choose('自动故障切换');
  await expect.poll(async()=>(await group()).now).toBe('自动故障切换');
  await page.screenshot({path:join(output,'dashboard-desktop.png'),fullPage:true,animations:'disabled'});
  await page.setViewportSize({width:390,height:844});
  // zashboard swaps desktop and mobile group components asynchronously;
  // wait for the mobile card before checking whether its node list is open.
  await expect(managedGroup.locator('.proxy-group-card')).toBeVisible();
  await choose(node);
  await expect.poll(async()=>(await group()).now).toBe(node);
  await page.screenshot({path:join(output,'dashboard-mobile.png'),fullPage:true,animations:'disabled'});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
  expect(errors.every(e=>e==='Failed to fetch')).toBe(true);
  expect(external.every(u=>u==='https://api.github.com/repos/Zephyruso/zashboard/releases/latest')).toBe(true);
  expect(websockets.some(p=>p.startsWith('/api/mihomo/'))).toBe(true);
  await writeFile(join(output,'result.json'),JSON.stringify({ok:true,nodeCount:initial.all.length-1,refreshedBackendToken:true,errors,external,websocketPaths:[...new Set(websockets)]},null,2));
  console.log('Real Console + native Mihomo + bundled zashboard browser checks passed.');
} finally {await browser.close();}
