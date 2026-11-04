import {createContext,useContext} from 'react';
import {Button,Field,Tooltip,tokens,type FieldProps} from '@fluentui/react-components';
import {ErrorCircleRegular} from '@fluentui/react-icons';

export const FieldErrors = createContext<Record<string,string>>({});

// Keep one small icon column in the control row. The Field owns the accessible
// error description, but its message is visually hidden, never a new text row.
export function ValidationField({name,error,label,children,...props}: Omit<FieldProps,'label'> & {name?:string;error?:string;label:string}) {
  const errors=useContext(FieldErrors), message=error || (name ? errors[name] : undefined);
  return <Field {...props} label={label} validationState={message?'error':'none'}
    validationMessage={message?{children:message,className:'field-error-description'}:undefined} validationMessageIcon={null}>
    {controlProps=><div className="validation-control"><div className="validation-input">{typeof children==='function'?children(controlProps):children}</div><span className="validation-icon">
      {message&&<Tooltip content={message} relationship="description"><Button type="button" size="small" appearance="transparent" icon={<ErrorCircleRegular style={{color:tokens.colorPaletteRedForeground1}}/>} aria-label={label+'：错误'}/></Tooltip>}
    </span></div>}
  </Field>;
}

export function socks5AddressError(value:string) {
  const address=value.trim();
  if(!address)return '请输入 SOCKS5 地址';
  const message='请输入有效的主机:端口，端口为 1～65535，例如 127.0.0.1:7891';
  const match=address.match(/^(\[[^\]]+\]|[^:\[\]]+):([0-9]+)$/);
  if(!match||Number(match[2])<1||Number(match[2])>65535)return message;
  const host=match[1];
  if(host.startsWith('[')) {
    // URL supplies the browser's IPv6 parser; zone identifiers are kept out of
    // URL parsing because Go's endpoint parser accepts scoped IPv6 addresses.
    const inside=host.slice(1,-1),parts=inside.split('%');
    if(parts.length>2||parts.length===2&&!parts[1])return message;
    try { const url=new URL('http://['+parts[0]+']/');if(!url.hostname.includes(':'))return message; }
    catch{return message;}
    return '';
  }
  if(host.includes('.')&&/^[0-9.]+$/.test(host)) {
    const octets=host.split('.');
    return octets.length===4&&octets.every(v=>/^(0|[1-9][0-9]{0,2})$/.test(v)&&Number(v)<=255)?'':message;
  }
  return host.length<=253&&host.replace(/\.$/,'').split('.').every(label=>label.length<=63&&/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/i.test(label))?'':message;
}

export function inputError(input:HTMLInputElement|HTMLSelectElement|HTMLTextAreaElement) {
  if(input.disabled)return '';
  if(input.name==='socks5Address')return socks5AddressError(input.value);
  if(input.name==='proxyImportText'&&input.value.trim())return '请先检查配置内容，或清除本次导入草稿';
  if(input.validity.valueMissing)return '请输入此项内容';
  if(input.validity.badInput)return '请输入有效数字';
  if(input instanceof HTMLInputElement) {
    if(input.validity.rangeUnderflow||input.validity.rangeOverflow)return `请输入 ${input.min}～${input.max} 范围内的数值`;
    if(input.validity.stepMismatch)return input.step==='1'?'请输入整数':`请输入以 ${input.step} 为步长的数值`;
  }
  return input.validity.valid?'':input.validationMessage;
}
