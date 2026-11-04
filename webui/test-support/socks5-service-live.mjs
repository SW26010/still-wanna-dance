import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:844}});
  await page.goto(base+'/#/network');
  const panel=page.locator('.panel').filter({has:page.getByRole('heading',{name:'SOCKS5 服务',exact:true})});
  await expect(panel).toContainText('服务：可用');
  await expect(panel.getByText('协商与认证通过',{exact:true})).toBeVisible();
  await expect(panel.locator('time')).toHaveCount(2);
  const address=await page.evaluate(async()=>{
    const token=JSON.parse(document.getElementById('swd-bootstrap').textContent).token;
    return (await (await fetch('/api/status',{headers:{'X-SWD-Token':token}})).json()).activeSettings.socks5Address;
  });
  await expect(panel.getByText(address,{exact:true})).toBeVisible();
  const before=await panel.locator('time').first().getAttribute('datetime');
  await panel.getByRole('button',{name:'重新检测 SOCKS5 服务',exact:true}).click();
  await expect.poll(()=>panel.locator('time').first().getAttribute('datetime'),{timeout:15000}).not.toBe(before);
  await expect(panel).toContainText('服务：可用');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,'real-console-socks5.png'),fullPage:true});
  await writeFile(join(output,'result.json'),JSON.stringify({service:await panel.textContent(),overflow:false},null,2)+'\n');
} finally {await browser.close();}
