import {expect, type Page} from '@playwright/test';

export async function saveSettings(page:Page, state:'待重启'|'已生效') {
  const actions=page.getByRole('region',{name:'设置操作',exact:true});
  await actions.getByRole('button',{name:'保存设置',exact:true}).click();
  // Disabled also means pending; the settled status proves the saved settings finished syncing.
  await expect(actions.getByRole('status')).toHaveText(state);
  await expect(actions.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
}
