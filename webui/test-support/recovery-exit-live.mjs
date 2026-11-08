import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [baseURL,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.platform==='win32'?'msedge':'chromium'});
const errors=[];
try {
  const pages=[];
  for(let i=0;i<2;i++){
    const page=await browser.newPage();
    page.on('pageerror',error=>errors.push(error.message));
    const connected=page.waitForEvent('websocket',{predicate:socket=>socket.url().endsWith('/api/lifecycle')}).then(socket=>socket.waitForEvent('framereceived'));
    await page.goto(baseURL);
    await connected;
    await expect(page.getByRole('status',{name:'后台程序',exact:true})).toHaveText('后台程序：启动失败');
    pages.push(page);
  }
  await pages[0].close();
  await expect(pages[1].getByRole('button',{name:'退出程序',exact:true})).toBeEnabled();
  await pages[1].screenshot({path:join(output,'recovery-before-close.png'),fullPage:true});
  await pages[1].close();
  expect(errors).toEqual([]);
} finally {
  await browser.close();
}
await writeFile(join(output,'browser.json'),JSON.stringify({pagesClosed:2,exitClicked:false,errors},null,2)+'\n');
