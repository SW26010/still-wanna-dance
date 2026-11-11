import { bootstrap } from './bootstrap';

export const hostsActionTimeout = 120_000;
export const hostsActionPending = '正在检查 hosts；需要修改时请允许系统管理员权限提示，最多等待 120 秒。';
export const unconfirmedAction = '操作结果尚未确认，后台可能仍在执行或已完成，请核对当前状态，勿重复提交。';
const requestTimeout = 15_000;

export function withRequestTimeout(signal: AbortSignal) {
  return AbortSignal.any([signal, AbortSignal.timeout(requestTimeout)]);
}

export class APIError extends Error {
  constructor(message: string, public status: number, public code?: string, public fields?:Record<string,string>) { super(message); }
}
export async function decode<T>(response: Response): Promise<T> {
  const text = await response.text();
  if (!response.ok) {
    let message = text.trim();
    let code: string|undefined;
    let fields:Record<string,string>|undefined;
    try { const data=JSON.parse(text);message=data.error||message;code=typeof data.code==='string'?data.code:undefined;
      if(data.fields&&typeof data.fields==='object'&&!Array.isArray(data.fields))fields=Object.fromEntries(Object.entries(data.fields).filter((entry):entry is [string,string]=>typeof entry[1]==='string'));
    } catch { /* Plain-text HTTP error. */ }
    throw new APIError(message || `HTTP ${response.status}`, response.status, code, fields);
  }
  return JSON.parse(text) as T;
}

function fetchAPI(path:string,body?:unknown,signal?:AbortSignal) {
  return fetch('/api/' + path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { 'Content-Type': 'application/json', 'X-SWD-Token': bootstrap.token },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: signal ?? AbortSignal.timeout(requestTimeout),
    cache: 'no-store',
  });
}

export async function request<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  return decode<T>(await fetchAPI(path,body,signal));
}

export async function requestBlob(path:string,signal:AbortSignal):Promise<Blob> {
  const response=await fetchAPI(path,undefined,signal);
  if(!response.ok)await decode<never>(response);
  return response.blob();
}

export async function downloadFile(path:string,filename:string,signal:AbortSignal):Promise<Headers> {
  const response=await fetchAPI(path,undefined,signal);
  if(!response.ok)await decode<never>(response);
  const content=await response.blob();
  signal.throwIfAborted();
  const url=URL.createObjectURL(content),link=document.createElement('a');
  try {
    link.href=url;link.download=filename;link.hidden=true;
    document.body.append(link);link.click();
  } finally {
    link.remove();setTimeout(()=>URL.revokeObjectURL(url),0);
  }
  return response.headers;
}
