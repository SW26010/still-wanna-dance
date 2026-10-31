import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
import {createServer} from 'node:http';
const [base,output,warning]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({locale:'zh-CN'}),errors=[],external=[],websockets=[],activeStreams=new Set();
  let documentGeneration=0;
  const managedGroup=page.frameLocator('iframe').locator('[data-group-name="SWD"]').first();
  const groupContent=()=>managedGroup.locator('.collapse-motion-content, .proxy-group-card-content');
  const choose=async name=>{
    if(!await groupContent().isVisible())await managedGroup.getByText('SWD',{exact:true}).click();
    await groupContent().getByText(name,{exact:true}).click();
    await expect(groupContent().getByText(name,{exact:true})).toHaveClass(/text-primary-content/);
  };
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{
    if(new URL(r.url()).origin!==base)external.push(r.url().split('?')[0]);
    // Playwright clears its WebSocket map on full document navigation without
    // emitting every old close event. Keep this probe scoped to the document;
    // hash navigation and iframe removal must still produce real close events.
    if(r.isNavigationRequest()&&r.frame()===page.mainFrame()){documentGeneration++;activeStreams.clear();}
  });
  page.on('websocket',ws=>{
    const path=new URL(ws.url()).pathname;websockets.push(path);
    if(path.startsWith('/api/mihomo/')){
      const generation=documentGeneration;
      // A socket event also reports rejected handshakes (the managed logs API).
      ws.on('framereceived',()=>{if(generation===documentGeneration&&!ws.isClosed())activeStreams.add(ws);});
      ws.on('close',()=>activeStreams.delete(ws));
      ws.on('socketerror',()=>activeStreams.delete(ws));
    }
  });
  // Upstream unconditionally checks its own release; it is unrelated to the
  // embedded node controls. Exercise those controls with the Internet offline.
  await page.context().route('https://**/*',route=>route.abort('internetdisconnected'));
  await page.goto(base+'/#/settings');
  const token=await page.locator('meta[name="swd-token"]').getAttribute('content');
  await page.getByRole('tab',{name:'网络',exact:true}).click();
  if(warning)await expect(page.getByText(warning,{exact:true})).toBeVisible();
  await expect(page.getByRole('link',{name:'打开代理面板',exact:true})).toHaveCount(0);
  const settingsURL=page.url();
  await page.getByRole('navigation',{name:'页面导航',exact:true}).getByRole('link',{name:'网络',exact:true}).click();
  await expect(page.getByRole('heading',{name:'连通性检测',exact:true})).toBeVisible();
  await expect(page.getByRole('heading',{name:'下载速度测试',exact:true})).toBeVisible();
  await page.getByRole('heading',{name:'代理面板',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('面板整组测速可能并发连接组内所有节点');
  await page.keyboard.press('Escape');
  const group=async()=>{const r=await page.request.get(base+'/api/mihomo/proxies/SWD',{headers:{Authorization:'Bearer '+token}});expect(r.ok()).toBe(true);return r.json();};
  await expect.poll(()=>page.evaluate(()=>localStorage.getItem('config/automatic-disconnection'))).toBe('false');
  await expect(managedGroup).toBeVisible();
  // Exercise both the initial frame and the frame recreated by Forward.
  const historyRoundTrips = 2;
  for (let visit = 0; visit < historyRoundTrips; visit++) {
    await page.goBack();
    await expect(page).toHaveURL(settingsURL);
    await expect(page.getByRole('tabpanel',{name:'网络',exact:true})).toBeVisible();
    await expect(page.locator('iframe')).toHaveCount(0);
    await page.goForward();
    await expect(page).toHaveURL(base+'/#/network');
    await expect(managedGroup).toBeVisible();
  }
  if(warning)await expect(page.locator('.proxy-dashboard').getByText(warning,{exact:true})).toBeVisible();
  const initial=await group(),node=initial.all[2];
  const {backendID,otherBackend}=await page.evaluate(()=>{
    const list=JSON.parse(localStorage.getItem('setup/api-list'));
    if(list.length!==1)throw Error('unexpected initial backends');
    list[0].password='expired-process-token';
    const backendID = list[0].uuid;
    const otherBackend = {...list[0], uuid:'other-fixture', secondaryPath:'/other-fixture'};
    list.unshift(otherBackend);
    localStorage.setItem('setup/api-list',JSON.stringify(list));
    localStorage.setItem('setup/active-uuid',otherBackend.uuid);
    return {backendID,otherBackend};
  });
  await page.reload();
  await expect(managedGroup).toBeVisible();
  const backends=await page.evaluate(()=>JSON.parse(localStorage.getItem('setup/api-list')));
  expect(backends).toHaveLength(2);
  expect(JSON.stringify(backends[0]) === JSON.stringify(otherBackend)).toBe(true);
  expect(backends[1].uuid).toBe(backendID);
  expect(backends[1].password === token).toBe(true);
  expect(await page.evaluate(()=>localStorage.getItem('setup/active-uuid'))).toBe(backendID);
  await page.screenshot({path:join(output,'dashboard-before-selection.png'),fullPage:true});
  await writeFile(join(output,'dashboard-body.txt'),await page.locator('body').innerText());
  await choose(node);
  await expect.poll(async()=>(await group()).now).toBe(node);
  await page.reload();
  await expect(managedGroup).toBeVisible();
  expect((await group()).now).toBe(node);
  await choose('自动故障切换');
  await expect.poll(async()=>(await group()).now).toBe('自动故障切换');
  // Native frame navigation must permit keyboard entry and return to SWD.
  await page.getByRole('link',{name:'独立窗口打开',exact:true}).focus();
  await page.keyboard.press('Tab');
  expect(await page.evaluate(()=>document.activeElement?.tagName)).toBe('IFRAME');
  // A frame can restore its previously focused node. Traverse back through
  // its actual tab order instead of assuming entry always starts at the top.
  let reverseTabs=0;
  while(await page.evaluate(()=>document.activeElement?.tagName==='IFRAME')&&reverseTabs<100){await page.keyboard.press('Shift+Tab');reverseTabs++;}
  expect(await page.evaluate(()=>document.activeElement?.tagName)).not.toBe('IFRAME');
  await page.getByRole('button',{name:'开始连通性检测',exact:true}).scrollIntoViewIfNeeded();
  await expect(managedGroup).toBeVisible();
  await page.getByRole('navigation',{name:'页面导航',exact:true}).getByRole('link',{name:'设置',exact:true}).click();
  await expect(page.locator('iframe')).toHaveCount(0);
  await expect.poll(()=>[...activeStreams].map(ws=>new URL(ws.url()).pathname)).toEqual([]);
  await page.getByRole('tab',{name:'网络',exact:true}).click();
  await page.getByRole('link',{name:'检测当前连接',exact:true}).click();
  await expect(page.locator('#network-tests')).toBeFocused();
  await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeVisible();
  await expect(managedGroup).toBeVisible();
  await expect.poll(()=>activeStreams.size).toBeGreaterThanOrEqual(3);
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});
  await expect(page.locator('iframe')).toHaveCount(0);
  await expect.poll(()=>activeStreams.size).toBe(0);
  expect((await group()).now).toBe('自动故障切换');
  await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});
  await expect(managedGroup).toBeVisible();
  const popupPromise=page.waitForEvent('popup');
  await page.getByRole('link',{name:'独立窗口打开',exact:true}).click();
  const popup=await popupPromise;
  await expect(popup.locator('[data-group-name="SWD"]').first()).toBeVisible();
  await popup.close();
  const damaged = await browser.newPage();
  await damaged.addInitScript(()=>localStorage.setItem('setup/api-list','invalid JSON'));
  await damaged.goto(base+'/#/network');
  await expect(damaged.frameLocator('iframe').getByRole('alert')).toContainText('无法读取或保存代理面板的连接设置');
  expect(await damaged.evaluate(()=>localStorage.getItem('setup/api-list'))).toBe('invalid JSON');
  await damaged.close();
  await page.screenshot({path:join(output,'dashboard-desktop.png'),fullPage:true,animations:'disabled'});
  await page.setViewportSize({width:390,height:844});
  // zashboard swaps desktop and mobile group components asynchronously;
  // wait for the mobile card before checking whether its node list is open.
  await expect(managedGroup.locator('.proxy-group-card')).toBeVisible();
  await choose(node);
  await expect.poll(async()=>(await group()).now).toBe(node);
  await page.screenshot({path:join(output,'dashboard-mobile.png'),fullPage:true,animations:'disabled'});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
  for(const width of [320,800]){
    await page.setViewportSize({width,height:844});
    await expect(managedGroup).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
    const frame=page.frames().find(frame=>frame.url().includes('/proxy/ui/'));
    expect(await frame.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
    await page.screenshot({path:join(output,`dashboard-${width}.png`),fullPage:true,animations:'disabled'});
  }
  // Small CSS viewports also exercise the layout constraints encountered when
  // zooming. These checks do not substitute for Windows DPI or screen readers.
  for(const layout of [
    {name:'dark',width:1280,height:720,colorScheme:'dark',forcedColors:'none'},
    {name:'contrast',width:320,height:844,colorScheme:'light',forcedColors:'active'},
    {name:'compact',width:640,height:360,colorScheme:'light',forcedColors:'none'},
    {name:'short',width:320,height:180,colorScheme:'light',forcedColors:'none'},
  ]){
    await page.setViewportSize({width:layout.width,height:layout.height});
    await page.emulateMedia({colorScheme:layout.colorScheme,forcedColors:layout.forcedColors,reducedMotion:'reduce'});
    await expect(managedGroup).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1)).toBe(true);
    await page.getByRole('button',{name:'开始连通性检测',exact:true}).scrollIntoViewIfNeeded();
    await page.locator('iframe').scrollIntoViewIfNeeded();
    const box=await page.locator('iframe').boundingBox();expect(box.width).toBeLessThanOrEqual(layout.width);expect(box.height).toBeGreaterThanOrEqual(320);
    await page.screenshot({path:join(output,`dashboard-${layout.name}.png`),fullPage:true,animations:'disabled'});
  }
  const foreign=createServer((_,response)=>{response.setHeader('Content-Type','text/html');response.end(`<iframe src="${base}/proxy/" title="cross-origin test"></iframe>`);});
  await new Promise(resolve=>foreign.listen(0,'127.0.0.1',resolve));
  try {
    const other=await browser.newPage(),violations=[];
    other.on('console',message=>{if(message.type()==='error')violations.push(message.text());});
    await other.goto(`http://127.0.0.1:${foreign.address().port}/`);
    await expect.poll(()=>violations.some(message=>message.includes("frame-ancestors 'self'"))).toBe(true);
    await other.close();
  } finally {await new Promise(resolve=>foreign.close(resolve));}
  expect(errors.every(e=>e==='Failed to fetch')).toBe(true);
  expect(external.every(u=>u==='https://api.github.com/repos/Zephyruso/zashboard/releases/latest')).toBe(true);
  expect(websockets.some(p=>p.startsWith('/api/mihomo/'))).toBe(true);
  await writeFile(join(output,'result.json'),JSON.stringify({ok:true,nodeCount:initial.all.length-1,embedded:true,historyRoundTrips,keyboardEntryAndExit:true,reverseTabs,streamsClosedOnNavigationAndHidden:true,crossOriginEmbeddingBlocked:true,standalone:true,refreshedBackendToken:true,preservedOtherBackend:true,preservedDamagedBackendList:true,errors,external,websocketPaths:[...new Set(websockets)]},null,2));
  console.log('Real Console + native Mihomo + bundled zashboard browser checks passed.');
} finally {await browser.close();}
