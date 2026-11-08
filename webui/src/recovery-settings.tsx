import {useEffect} from 'react';
import {Spinner} from '@fluentui/react-components';
import {useApp,useResource} from './hooks';
import {Feedback,useThemePreference} from './ui';
import {bootstrap} from './bootstrap';
import {SettingsAndNetwork} from './settings-page';
import {SettingsSource,useSettingsDraft,stagedTheme,type SettingsSnapshot} from './settings-source';

// Recovery changes the source and transaction scope, never the settings form.
export function RecoverySettings({visible,anchor,onTabSelect}:{visible:boolean;anchor:string;onTabSelect:(anchor:string)=>void}) {
  const app=useApp(), theme=useThemePreference(), draft=useSettingsDraft();
  const resource=useResource<SettingsSnapshot&{unavailable?:string}>('recovery/settings',!app.stopped,app.revision,0);
  useEffect(()=>{
    if(!draft.draft[0])draft.errors[1](resource.data?.fields||{});
    const value=resource.data?.settings?.theme;
    if(value==='system'||value==='light'||value==='dark')theme.setPreference(value);
  },[resource.data]);
  const unavailable=app.busy||app.stopped||!!resource.error||!!resource.data?.unavailable;
  const error=resource.error||resource.data?.unavailable;
  return <div hidden={!visible}>
    <Feedback error>{error!==bootstrap.recovery?.reason?error:undefined}</Feedback>
    {!resource.data&&!resource.error&&<Spinner label="正在读取配置…"/>}
    {resource.data?.settings&&<SettingsSource.Provider value={{data:resource.data,loadedRevision:resource.loadedRevision,unavailable,draft,scope:'workspace',selectTab:onTabSelect,theme:stagedTheme(draft,resource.data.settings.theme)}}>
      <SettingsAndNetwork settingsOnly settingsVisible={visible} networkVisible={false} anchor={anchor} onTabSelect={onTabSelect}/>
    </SettingsSource.Provider>}
  </div>;
}
