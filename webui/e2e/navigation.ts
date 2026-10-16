import {expect,type Page} from '@playwright/test';

export async function navigate(page:Page,name:string) {
  const toggle=page.getByRole('button',{name:'展开控制台页面',exact:true});
  if(page.viewportSize()!.width<=640)await toggle.click();
  await page.getByRole('navigation',{name:'控制台页面',exact:true}).getByRole('link',{name,exact:true}).click();
  await expect(page.getByRole('heading',{level:1,name:name==='开始使用'?'Load failed? But I still wanna dance!':name,exact:true})).toBeVisible();
  if(page.viewportSize()!.width<=640)await expect(toggle).toBeVisible();
}
export async function navigateStep(page:Page,name:string) {
  const toggle=page.getByRole('button',{name:'展开引导步骤',exact:true});
  if(page.viewportSize()!.width<=640)await toggle.click();
  await page.getByRole('navigation',{name:'引导步骤',exact:true}).getByRole('button',{name,exact:true}).click();
  await expect(page.getByRole('heading',{level:1,name:name.slice(3),exact:true})).toBeVisible();
  if(page.viewportSize()!.width<=640)await expect(toggle).toBeVisible();
}
