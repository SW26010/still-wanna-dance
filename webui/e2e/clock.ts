import {expect,type Page} from '@playwright/test';

// Keep time moving while async responses register the next polling timer.
// The normal assertion deadline still catches polling that never resumes.
export function expectWithClock<T>(page:Page,actual:()=>T|Promise<T>) {
  return expect.poll(async()=>{
    await page.clock.fastForward(1000);
    return actual();
  },{intervals:[100]});
}
