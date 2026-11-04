import {navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('desktop pages use a left navigation with native links and arrow keys',async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width:1280,height:900});
  for(const route of ['/#/home','/help','/about','/terms']){
    await page.goto(route);
    const nav=page.getByRole('navigation',{name:'页面导航',exact:true});
    await expect(nav).toHaveCount(1);await expect(nav).toBeVisible();
    const heading=page.getByRole('heading',{level:1}).first();await expect(heading).toBeVisible();
    const left=await nav.boundingBox(),content=await heading.boundingBox();
    expect(left!.x+left!.width).toBeLessThanOrEqual(content!.x);
    expect(left!.width).toBeCloseTo(144,1);
    const items=nav.getByRole('link');
    await items.first().focus();await page.keyboard.press('ArrowDown');await expect(items.nth(1)).toBeFocused();
    {await expect(nav.getByRole('link',{name:'设置',exact:true})).toHaveAttribute('href','#/settings');}
    await page.screenshot({path:info.outputPath(route.includes('/#')?'console.png':route.slice(1)+'.png'),fullPage:true,animations:'disabled'});
  }
});

for(const narrow of [false,true])test(`${narrow?'overlay':'desktop'} navigation shrinks its gap before scrolling every entry together`,async({page},info)=>{
  await mockAPI(page);
  await page.emulateMedia({colorScheme:narrow?'dark':'light',reducedMotion:'reduce'});
  await page.setViewportSize({width:narrow?390:1280,height:900});await page.goto('/#/licenses');
  const nav=page.getByRole('navigation',{name:'页面导航',exact:true});
  if(narrow)await page.getByRole('button',{name:'展开页面导航',exact:true}).click();
  const sizes=narrow?[[390,844],[390,360],[320,225]]:[[1280,900],[700,520],[700,380],[700,240]];
  let sawSpace=false,sawOverflow=false;
  for(const [width,height] of sizes){
    await page.setViewportSize({width,height});
    await expect(nav).toBeVisible();
    if(!narrow)await expect.poll(()=>page.locator('.navigation-sidebar').evaluate(el=>el.getBoundingClientRect().bottom)).toBeCloseTo(height-24,1);
    const body=nav.locator('.navigation-items'),links=nav.getByRole('link');
    await expect(links).toHaveText(['加速','历史','曲库','网络','设置','关于']);
    await expect(nav.getByRole('separator')).toHaveCount(0);
    await links.first().focus();
    const metrics=await body.evaluate(el=>{
      const items=[...el.querySelectorAll('a')].map(item=>item.getBoundingClientRect());
      const rect=el.getBoundingClientRect(),style=getComputedStyle(el);
      return {overflow:el.scrollHeight>el.clientHeight+1,gap:items[4].top-items[3].bottom,normalGap:items[1].top-items[0].bottom,
        lastBottom:items[5].bottom,bottom:rect.bottom-parseFloat(style.paddingBottom),heights:items.map(item=>item.height)};
    });
    expect(metrics.heights).toEqual(Array(6).fill(metrics.heights[0]));
    expect(metrics.heights[0]).toBeGreaterThanOrEqual(40);
    if(metrics.overflow){
      sawOverflow=true;expect(metrics.gap).toBeCloseTo(metrics.normalGap,1);
    }else{
      sawSpace ||= metrics.gap>metrics.normalGap+1;
      expect(metrics.lastBottom).toBeCloseTo(metrics.bottom,1);
    }
    for(let i=1;i<6;i++){await page.keyboard.press('ArrowDown');await expect(links.nth(i)).toBeFocused();}
    if(metrics.overflow){
      expect(await body.evaluate(el=>el.scrollTop)).toBeGreaterThan(0);
      const bounds=await body.boundingBox(),last=await links.last().boundingBox(),first=await links.first().boundingBox();
      expect(last!.y+last!.height).toBeLessThanOrEqual(bounds!.y+bounds!.height+1);
      expect(first!.y).toBeLessThan(bounds!.y);
    }
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:info.outputPath(`${width}x${height}.png`),animations:'disabled'});
    await page.keyboard.press('ArrowDown');await expect(links.first()).toBeFocused();
  }
  expect(sawSpace).toBe(true);expect(sawOverflow).toBe(true);
});

test('narrow left drawer traps focus, restores trigger on Escape, and follows selected routes',async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width:390,height:844});await page.emulateMedia({colorScheme:'dark'});await page.goto('/#/settings');
  const toggle=page.getByRole('button',{name:'展开页面导航',exact:true}),nav=page.getByRole('navigation',{name:'页面导航',exact:true});
  await toggle.click();await expect(nav).toBeVisible();
  await expect.poll(async()=>(await nav.boundingBox())?.x).toBeCloseTo(0,1);expect((await nav.boundingBox())!.width).toBeCloseTo(260,1);
  await expect(nav.getByRole('link',{name:'设置',exact:true})).toHaveAttribute('aria-current','page');
  for(let i=0;i<12;i++){await page.keyboard.press('Tab');expect(await nav.evaluate(el=>el.contains(document.activeElement))).toBe(true);}
  await page.screenshot({path:info.outputPath('drawer-dark.png'),animations:'disabled'});
  await page.keyboard.press('Escape');await expect(nav).toHaveCount(0);await expect(toggle).toBeFocused();
  await navigate(page,'曲库');await expect(page.locator('#main')).toBeFocused();
  await toggle.click();await expect(nav.getByRole('link',{name:'曲库',exact:true})).toHaveAttribute('aria-current','page');
  await page.setViewportSize({width:1280,height:900});await expect(toggle).toHaveCount(0);await expect(nav).toBeVisible();
  await page.setViewportSize({width:320,height:800});await expect(nav).toHaveCount(0);await expect(toggle).toBeVisible();
  await page.emulateMedia({forcedColors:'active',reducedMotion:'reduce'});await toggle.click();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('drawer-forced-colors.png'),animations:'disabled'});
});

test('desktop sidebar toggles by keyboard without losing focus or content',async({page},info)=>{
  await mockAPI(page);await page.setViewportSize({width:1280,height:900});
  await page.emulateMedia({colorScheme:'light'});
  await page.goto('/#prepareAheadSeconds');
  const label='页面导航',nav=page.getByRole('navigation',{name:label,exact:true});
  const collapse=page.getByRole('button',{name:'收起'+label,exact:true}),expand=page.getByRole('button',{name:'展开'+label,exact:true});
  const heading=page.getByRole('heading',{level:1}).first(),draft=page.getByRole('spinbutton',{name:/提前准备时间/});
  await expect(nav).toBeVisible();const expandedX=(await heading.boundingBox())!.x;
  await draft.fill('777');
  await collapse.focus();await page.keyboard.press('Space');
  await expect(nav).toHaveCount(0);await expect(expand).toBeFocused();await expect(expand).toHaveAttribute('aria-expanded','false');
  expect((await heading.boundingBox())!.x).toBeLessThan(expandedX-100);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.evaluate(()=>window.scrollTo(0,0));
  await page.screenshot({path:info.outputPath('collapsed.png'),animations:'disabled'});
  {
    await page.evaluate(()=>{location.hash='/help';});
    await page.getByRole('tab',{name:'介绍',exact:true}).click();
    await expect(page.getByRole('heading',{level:1})).toHaveText('关于');
    await expect(nav).toHaveCount(0);
    await page.goBack();await page.goBack();await expect(draft).toHaveValue('777');await expect(nav).toHaveCount(0);
    await page.setViewportSize({width:390,height:844});await expand.click();await expect(nav).toBeVisible();
    await page.setViewportSize({width:1280,height:900});await expect(nav).toHaveCount(0);await expect(expand).toBeVisible();
  }
  await expand.focus();await page.keyboard.press('Enter');
  await expect(nav).toBeVisible();await expect(collapse).toBeFocused();await expect(collapse).toHaveAttribute('aria-expanded','true');
  await expect.poll(async()=>(await heading.boundingBox())!.x).toBeCloseTo(expandedX,1);
  await page.keyboard.press('Tab');
  expect(await nav.evaluate(el=>el.contains(document.activeElement))).toBe(true);
  await expect(draft).toHaveValue('777');
  await page.evaluate(()=>window.scrollTo(0,0));
  await page.screenshot({path:info.outputPath('expanded.png'),animations:'disabled'});
});
