import {chromium} from '@playwright/test';
import {createRequire} from 'node:module';

export const browserChannel=process.platform==='win32'?'msedge':'chromium';
export const launchBrowser=options=>chromium.launch({channel:browserChannel,...options});

export default async function browserEnvironment(config){
  const project=config.projects[0],browser=await launchBrowser({headless:project.use.headless!==false});
  try {
    const require=createRequire(import.meta.url);
    console.log('Browser test environment: '+JSON.stringify({
      platform:process.platform,node:process.version,playwright:require('@playwright/test/package.json').version,
      channel:browserChannel,browser:browser.version(),workers:config.workers,retries:project.retries,
    }));
  } finally { await browser.close(); }
}
