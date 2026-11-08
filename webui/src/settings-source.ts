import {createContext} from 'react';
import {useApp} from './hooks';
import {useThemePreference} from './ui';
import {useSettingsController} from './settings-controller';
import type {Settings,Status} from './types';

export type SettingsSnapshot = Pick<Status,'settings'|'restartRequired'|'defaultDatabasePath'|'defaultVideoDir'|'defaultBlockCacheDir'|'portableDir'|'defaultLogDir'|'proxyNodes'|'socks5CredentialError'|'actionErrors'|'storageConfirmation'|'storageIssues'> & {fields?:Record<string,string>};
export type SettingsEditor = ReturnType<typeof useSettingsController> & {
 data:SettingsSnapshot;
 selectTab?:(anchor:string)=>void;
 theme:{value:string;change:(value:Settings['theme'])=>void};
};
export const SettingsSource=createContext<SettingsEditor|null>(null);

export function useNormalSettingsEditor():SettingsEditor {
 const app=useApp(),theme=useThemePreference();
 const data=app.status.data!;
 const editor=useSettingsController(data,app.status.loadedRevision,app.unavailable,'section');
 return {...editor,data,theme:{value:theme.preference,change:async value=>{
   if(await app.action('settings/theme',{theme:value}))theme.setPreference(value);
 }}};
}
