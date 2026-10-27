import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

for(const [endpoint,label] of [['response-sources','刷新来源统计'],['latency','刷新首正文统计']] as const)test(`${label} rotates during the request and settles after success or failure`,async({page})=>{
  await mockAPI(page);
  await page.clock.install();await page.emulateMedia({reducedMotion:'no-preference'});
  let reads=0,hold=false,fail=false,release=()=>{};
  await page.route('**/api/'+endpoint,async route=>{
    reads++;
    if(hold)await new Promise<void>(resolve=>{release=resolve;});
    if(fail)await route.fulfill({status:503,body:'unavailable'});
    else await route.fallback();
  });
  await page.goto('/#/timeline');
  const button=page.getByRole('button',{name:label,exact:true}),icon=button.locator('svg');
  await expect(button).toBeEnabled();
  expect(await icon.evaluate(el=>el.getAnimations().length)).toBe(0);
  for(const failed of [false,true]){
    fail=failed;hold=true;
    const before=await button.boundingBox(),count=reads;
    await button.focus();await button.press('Enter');
    await expect.poll(()=>reads).toBe(count+1);
    const rotation=await icon.evaluate(el=>getComputedStyle(el).transform);
    await expect.poll(()=>icon.evaluate(el=>getComputedStyle(el).transform)).not.toBe(rotation);
    await expect(button).toHaveAttribute('aria-busy','true');await expect(button).toBeDisabled();await expect(button).toBeFocused();
    expect(await button.boundingBox()).toEqual(before);
    await button.press('Enter');expect(reads).toBe(count+1);
    await page.emulateMedia({reducedMotion:'reduce'});
    expect(await icon.evaluate(el=>el.getAnimations().length)).toBe(0);
    await expect(button).toHaveAttribute('aria-busy','true');
    await page.emulateMedia({reducedMotion:'no-preference'});
    await expect.poll(()=>icon.evaluate(el=>el.getAnimations().length)).toBe(1);
    hold=false;release();
    await expect(button).toBeEnabled();await expect(button).toHaveAttribute('aria-busy','false');
    expect(await icon.evaluate(el=>el.getAnimations().length)).toBe(0);
    await expect(button).toBeFocused();
  }
});
