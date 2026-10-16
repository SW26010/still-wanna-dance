import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate,navigateStep} from './navigation';

test('desktop pages and wizard use a left navigation with native links and arrow keys',async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width:1280,height:900});
  for(const route of ['/#/home','/help','/about','/terms','/#/setup']){
    await page.goto(route);
    const wizard=route.endsWith('setup'),nav=page.getByRole('navigation',{name:wizard?'引导步骤':'控制台页面',exact:true});
    await expect(nav).toHaveCount(1);await expect(nav).toBeVisible();
    const heading=page.getByRole('heading',{level:1}).first();await expect(heading).toBeVisible();
    const left=await nav.boundingBox(),content=await heading.boundingBox();
    expect(left!.x+left!.width).toBeLessThanOrEqual(content!.x);
    expect(left!.width).toBeCloseTo(260,1);
    const items=nav.getByRole(wizard?'button':'link');
    await items.first().focus();await page.keyboard.press('ArrowDown');await expect(items.nth(1)).toBeFocused();
    if(!wizard){await expect(nav.getByRole('link',{name:'设置',exact:true})).toHaveAttribute('href',route.startsWith('/#')?'#/settings':'/#/settings');}
    await page.screenshot({path:info.outputPath(wizard?'wizard.png':route.includes('/#')?'console.png':route.slice(1)+'.png'),fullPage:true,animations:'disabled'});
  }
  await navigateStep(page,'3. 网络');await expect(page.getByRole('button',{name:'3. 网络',exact:true})).toHaveAttribute('aria-current','step');
  await page.getByRole('checkbox',{name:'直连',exact:true}).uncheck();
  await page.getByRole('button',{name:'4. 启用',exact:true}).click();
  await expect(page.getByRole('heading',{level:1,name:'网络',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'3. 网络',exact:true})).toHaveAttribute('aria-current','step');
});

test('narrow left drawer traps focus, restores trigger on Escape, and follows selected routes',async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width:390,height:844});await page.emulateMedia({colorScheme:'dark'});await page.goto('/#/settings');
  const toggle=page.getByRole('button',{name:'展开控制台页面',exact:true}),nav=page.getByRole('navigation',{name:'控制台页面',exact:true});
  await toggle.click();await expect(nav).toBeVisible();
  await expect.poll(async()=>(await nav.boundingBox())?.x).toBeCloseTo(0,1);expect((await nav.boundingBox())!.width).toBeCloseTo(260,1);
  await expect(nav.getByRole('link',{name:'设置',exact:true})).toHaveAttribute('aria-current','page');
  for(let i=0;i<12;i++){await page.keyboard.press('Tab');expect(await nav.evaluate(el=>el.contains(document.activeElement))).toBe(true);}
  await page.screenshot({path:info.outputPath('drawer-dark.png'),animations:'disabled'});
  await page.keyboard.press('Escape');await expect(nav).toHaveCount(0);await expect(toggle).toBeFocused();
  await navigate(page,'运行状态');await expect(page.locator('#main')).toBeFocused();
  await toggle.click();await expect(nav.getByRole('link',{name:'运行状态',exact:true})).toHaveAttribute('aria-current','page');
  await page.setViewportSize({width:1280,height:900});await expect(toggle).toHaveCount(0);await expect(nav).toBeVisible();
  await page.setViewportSize({width:320,height:800});await expect(nav).toHaveCount(0);await expect(toggle).toBeVisible();
  await page.emulateMedia({forcedColors:'active',reducedMotion:'reduce'});await toggle.click();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('drawer-forced-colors.png'),animations:'disabled'});
});
