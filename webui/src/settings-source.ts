import {createContext,useState} from 'react';
import {useApp} from './hooks';
import {useThemePreference} from './ui';
import type {Settings,Status} from './types';
import type {Draft} from './settings';
import type {LocationValidation} from './storage-directory';

// The editor needs saved settings and location metadata, not a running engine.
export type SettingsSnapshot = Pick<Status,'settings'|'restartRequired'|'defaultDatabasePath'|'defaultVideoDir'|'defaultBlockCacheDir'|'portableDir'|'defaultLogDir'|'proxyNodes'|'socks5CredentialError'|'actionErrors'|'storageConfirmation'|'storageIssues'> & {fields?:Record<string,string>};
export function useSettingsDraft() {
  return {
    draft:useState<Partial<Draft>>(), pending:useState(false), revision:useState<number>(),
    locationError:useState(''), errors:useState<Record<string,string>>({}), location:useState<LocationValidation>({}),
  };
}
export interface SettingsEditor {
  data:SettingsSnapshot;
  loadedRevision?:number;
  unavailable:boolean;
  draft:ReturnType<typeof useSettingsDraft>;
  scope:'section'|'workspace';
  selectTab?:(anchor:string)=>void;
  theme:{value:string;change:(value:Settings['theme'])=>void};
}
export const SettingsSource = createContext<SettingsEditor|null>(null);

// Normal application: independent section drafts and immediate theme saving.
export function useNormalSettingsEditor():SettingsEditor {
  const app=useApp(), theme=useThemePreference(), draft=useSettingsDraft();
  return {
    data:app.status.data!,loadedRevision:app.status.loadedRevision,unavailable:app.unavailable,
    draft,scope:'section',theme:{value:theme.preference,change:async value=>{
      if(await app.action('settings/theme',{theme:value}))theme.setPreference(value);
    }},
  };
}

// Recovery stages appearance with all other repairs in the shared transaction.
export function stagedTheme(draft:ReturnType<typeof useSettingsDraft>,saved:Settings['theme']):SettingsEditor['theme'] {
  return {value:draft.draft[0]?.theme??saved,change:value=>{
    draft.draft[1](previous=>({...previous,theme:value}));
    draft.errors[1](previous=>{const next={...previous};delete next.theme;return next;});
  }};
}
