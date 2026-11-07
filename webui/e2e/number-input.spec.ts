import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

for (const [width,theme] of [[1280,'light'],[320,'dark'],[320,'forced']] as const) {
  test(`settings numbers share Fluent steps and unit layout at ${width}px in ${theme}`,async({page},info)=>{
    await mockAPI(page);
    await page.setViewportSize({width,height:1000});
    await page.emulateMedia({colorScheme:theme==='dark'?'dark':'light',forcedColors:theme==='forced'?'active':'none'});
    await page.goto('/#/settings');
    for (const [section,name,stepLabel,unit,start,min,max,choice] of [
      ['常规','请求明细保留天数','请求明细保留天数','天',90,1,36500,'保留指定天数'],
      ['存储','视频存储预算（GiB）','视频预算','GiB',12.5,0.01,8388607,'限制为'],
      ['下载与缓存','同时下载数量','同时下载数量','',20,1,64,''],
      ['下载与缓存','并发增长间隔（毫秒）','并发增长间隔','毫秒',350,0,2000,''],
      ['下载与缓存','提前准备时间（秒）','提前准备时间','秒',900,100,16000,''],
      ['下载与缓存','延续保护时间（秒）','延续保护时间','秒',400,0,10000,''],
    ] as const) {
      await page.getByRole('tab',{name:section,exact:true}).click();
      if(choice)await page.getByRole('radio',{name:choice,exact:true}).check();
      const input=page.getByRole('spinbutton',{name,exact:true});
      const up=page.getByRole('button',{name:'增加'+stepLabel,exact:true});
      const down=page.getByRole('button',{name:'减少'+stepLabel,exact:true});
      await input.fill(String(start));
      await up.click();await expect(input).toHaveValue(String(start+1));
      await down.click();await expect(input).toHaveValue(String(start));
      await input.press('ArrowUp');await expect(input).toHaveValue(String(start+1));
      await input.press('ArrowDown');await expect(input).toHaveValue(String(start));
      await input.fill(String(min));await input.blur();await expect(down).toBeDisabled();
      await input.fill(String(max));await input.blur();await expect(up).toBeDisabled();
      await input.fill(String(start));await input.blur();
      const control=input.locator('..');
      await expect(control).toHaveClass(/fui-SpinButton/);
      expect((await control.boundingBox())!.height).toBe(32);
      if(unit) {
        const suffix=control.locator('..').getByText(unit,{exact:true});
        await expect(suffix).toBeVisible();
        expect((await suffix.boundingBox())!.x).toBeGreaterThan((await control.boundingBox())!.x+(await control.boundingBox())!.width);
      }
      expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
      await control.locator('..').screenshot({path:info.outputPath(`${stepLabel}.png`),animations:'disabled'});
    }
    await page.locator('#download-settings').screenshot({path:info.outputPath('download-settings.png'),animations:'disabled'});
  });
}
