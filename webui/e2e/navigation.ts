import {expect,type Page} from '@playwright/test';

export async function navigate(page:Page,name:string) {
  if(name==='帮助'||name==='使用条款'){
    await navigate(page,'关于');await page.getByRole('tab',{name:name==='帮助'?'使用帮助':'用户协议',exact:true}).click();return;
  }
  const toggle=page.getByRole('button',{name:'展开控制台页面',exact:true});
  if(page.viewportSize()!.width<=640)await toggle.click();
  await page.getByRole('navigation',{name:'控制台页面',exact:true}).getByRole('link',{name,exact:true}).click();
  await expect(page.getByRole('heading',{level:1,name,exact:true})).toBeVisible();
  if(page.viewportSize()!.width<=640)await expect(toggle).toBeVisible();
}
export async function navigateSettings(page:Page,tab:string) {
  await navigate(page,'设置');await page.getByRole('tab',{name:tab,exact:true}).click();
  await expect(page.getByRole('tab',{name:tab,exact:true})).toHaveAttribute('aria-selected','true');
}
export async function navigateStep(page:Page,name:string) {
  const toggle=page.getByRole('button',{name:'展开引导步骤',exact:true});
  if(page.viewportSize()!.width<=640)await toggle.click();
  await page.getByRole('navigation',{name:'引导步骤',exact:true}).getByRole('button',{name,exact:true}).click();
  await expect(page.getByRole('heading',{level:1,name:name.slice(3),exact:true})).toBeVisible();
  if(page.viewportSize()!.width<=640)await expect(toggle).toBeVisible();
}
