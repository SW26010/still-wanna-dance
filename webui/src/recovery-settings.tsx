import {useEffect} from 'react';
import {Spinner} from '@fluentui/react-components';
import {useApp,useResource} from './hooks';
import {Feedback,useThemePreference} from './ui';
import {bootstrap} from './bootstrap';
import {useSettingsController} from './settings-controller';
import {SettingsAndNetwork} from './settings-page';
import {SettingsSource,type SettingsSnapshot} from './settings-source';

// Recovery changes the source and transaction scope, never the settings form.
export function RecoverySettings({visible,anchor,onTabSelect}:{visible:boolean;anchor:string;onTabSelect:(anchor:string)=>void}) {
  const app=useApp(), theme=useThemePreference();
  // Keep the editor mounted on read failure so its unsaved draft survives.
  const resource=useResource<SettingsSnapshot&{unavailable?:string}>('recovery/settings',!app.stopped,app.revision,0,true);
  useEffect(()=>{
    const value=resource.data?.settings?.theme;
    if(value==='system'||value==='light'||value==='dark')theme.setPreference(value);
  },[resource.data]);
  const unavailable=app.busy||app.stopped||!!resource.error||!!resource.data?.unavailable;
  const error=resource.error||resource.data?.unavailable;
  return <div hidden={!visible}>
    <Feedback error>{error!==bootstrap.recovery?.reason?error:undefined}</Feedback>
    {!resource.data&&!resource.error&&<Spinner label="正在读取配置…"/>}
    {resource.data?.settings&&<RecoveryEditor data={resource.data} loadedRevision={resource.loadedRevision} unavailable={unavailable} visible={visible} anchor={anchor} onTabSelect={onTabSelect}/>}

  </div>;
}

function RecoveryEditor({data,loadedRevision,unavailable,visible,anchor,onTabSelect}:{data:SettingsSnapshot;loadedRevision?:number;unavailable:boolean;visible:boolean;anchor:string;onTabSelect:(anchor:string)=>void}) {
 const editor=useSettingsController(data,loadedRevision,unavailable,'workspace');
 return <SettingsSource.Provider value={{...editor,data,selectTab:onTabSelect,theme:{value:editor.values.theme??data.settings.theme,change:theme=>editor.update({theme})}}}>
   <SettingsAndNetwork settingsOnly settingsVisible={visible} networkVisible={false} anchor={anchor} onTabSelect={onTabSelect}/>
 </SettingsSource.Provider>;
}
