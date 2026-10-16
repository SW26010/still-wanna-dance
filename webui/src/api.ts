import { bootstrap } from './bootstrap';

export const hostsActionTimeout = 120_000;
export const hostsActionPending = '正在检查 hosts；需要修改时请允许系统管理员权限提示，最多等待 120 秒。';
export const unconfirmedAction = '操作结果尚未确认，后台可能仍在执行或已完成，请核对当前状态，勿重复提交。';

export class APIError extends Error {
  constructor(message: string, public status: number) { super(message); }
}
export async function decode<T>(response: Response): Promise<T> {
  const text = await response.text();
  if (!response.ok) {
    let message = text.trim();
    try { message = JSON.parse(text).error || message; } catch { /* Plain-text HTTP error. */ }
    throw new APIError(message || `HTTP ${response.status}`, response.status);
  }
  return JSON.parse(text) as T;
}

export async function request<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch('/api/' + path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { 'Content-Type': 'application/json', 'X-SWD-Token': bootstrap.token },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: signal ?? AbortSignal.timeout(15000),
    cache: 'no-store',
  });
  return decode<T>(response);
}
