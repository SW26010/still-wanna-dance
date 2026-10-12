// The opt-in UI preview uses the same fixed dialog script as desktop.SelectFolder.
import {spawn} from 'node:child_process';
import {readFile} from 'node:fs/promises';
import {win32} from 'node:path';

export async function selectPreviewFolder(kind, initial, signal) {
  const title = {storage:'选择视频缓存文件夹',import:'选择已有视频文件夹'}[kind];
  if (!title || typeof initial !== 'string' || initial.length > 4096 || initial.includes('\0') || (initial && !win32.isAbsolute(initial))) throw Error('无效的文件夹选择请求');
  if (process.platform !== 'win32' || !win32.isAbsolute(process.env.SystemRoot || '')) throw Error('目录选择需要 Windows');
  const script = await readFile(new URL('../internal/desktop/folder_dialog.ps1',import.meta.url),'utf8');
  return new Promise((resolve,reject) => {
    const child = spawn(win32.join(process.env.SystemRoot,'System32','WindowsPowerShell','v1.0','powershell.exe'),['-NoProfile','-NonInteractive','-STA','-Command',script],{windowsHide:true,signal});
    let output = '';
    child.stdout.setEncoding('utf8');
    child.stdout.on('data',data => {output += data;if(output.length > 16384)child.kill();});
    child.stderr.resume();
    child.stdin.on('error',()=>{});
    child.on('error',reject);
    child.on('close',code => {
      if (code !== 0) return reject(Error('无法打开 Windows 文件夹选择器，请重试'));
      try {const result = JSON.parse(output);if(typeof result.path !== 'string')throw Error('invalid path');resolve({path:result.path,canceled:!result.path});}
      catch {reject(Error('无法读取所选文件夹'));}
    });
    child.stdin.end(JSON.stringify({title,initial}));
  });
}
