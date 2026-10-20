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
    if(!wizard){await expect(nav.getByRole('link',{name:'设置',exact:true})).toHaveAttribute('href','#/settings');}
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

for(const wizard of [false,true])test(`desktop sidebar toggles by keyboard without losing focus or content (${wizard?'wizard':'console'})`,async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width:1280,height:900});
  await page.emulateMedia({colorScheme:wizard?'dark':'light'});
  await page.goto(wizard?'/#/setup':'/#/settings');
  const label=wizard?'引导步骤':'控制台页面',nav=page.getByRole('navigation',{name:label,exact:true});
  const collapse=page.getByRole('button',{name:'收起'+label,exact:true}),expand=page.getByRole('button',{name:'展开'+label,exact:true});
  const heading=page.getByRole('heading',{level:1}).first(),draft=page.getByRole('spinbutton',{name:/提前准备时间/});
  await expect(nav).toBeVisible();const expandedX=(await heading.boundingBox())!.x;
  if(!wizard)await draft.fill('777');
  await collapse.focus();await page.keyboard.press('Space');
  await expect(nav).toHaveCount(0);await expect(expand).toBeFocused();await expect(expand).toHaveAttribute('aria-expanded','false');
  expect((await heading.boundingBox())!.x).toBeLessThan(expandedX-100);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.evaluate(()=>window.scrollTo(0,0));
  await page.screenshot({path:info.outputPath('collapsed.png'),animations:'disabled'});
  if(!wizard){
    await page.locator('footer').getByRole('link',{name:'关于',exact:true}).click();
    await expect(page.getByRole('heading',{level:1})).toHaveText('Load failed? But I still wanna dance!');
    await expect(nav).toHaveCount(0);
    await page.goBack();await expect(draft).toHaveValue('777');await expect(nav).toHaveCount(0);
    await page.setViewportSize({width:390,height:844});await expand.click();await expect(nav).toBeVisible();
    await page.setViewportSize({width:1280,height:900});await expect(nav).toHaveCount(0);await expect(expand).toBeVisible();
  }
  await expand.focus();await page.keyboard.press('Enter');
  await expect(nav).toBeVisible();await expect(collapse).toBeFocused();await expect(collapse).toHaveAttribute('aria-expanded','true');
  await expect.poll(async()=>(await heading.boundingBox())!.x).toBeCloseTo(expandedX,1);
  await page.keyboard.press('Tab');
  expect(await nav.evaluate(el=>el.contains(document.activeElement))).toBe(true);
  if(!wizard)await expect(draft).toHaveValue('777');
  await page.evaluate(()=>window.scrollTo(0,0));
  await page.screenshot({path:info.outputPath('expanded.png'),animations:'disabled'});
});
