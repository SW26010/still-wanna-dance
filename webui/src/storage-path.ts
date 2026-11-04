import { bootstrap } from './bootstrap';
import { samePath } from './path-picker';
import type { Status } from './types';

// Format only the default locations. Keep resolved paths in drafts and requests.
export function displayStoragePath(path: string, status: Status) {
  if (!path) return path;
  const configDir = bootstrap.paths?.Config?.replace(/[/\\][^/\\]*$/, '');
  const defaults = [
    [status.defaultDatabasePath, configDir],
    [status.defaultVideoDir, configDir],
    [status.defaultBlockCacheDir, status.portableDir],
  ];
  for (const [defaultPath, base] of defaults) {
    if (!defaultPath || !base || !samePath(path, defaultPath)) continue;
    const prefix = base.replace(/[/\\]+$/, '').replaceAll('\\', '/') + '/';
    if (defaultPath.replaceAll('\\', '/').toLowerCase().startsWith(prefix.toLowerCase())) return '.' + defaultPath.slice(prefix.length - 1);
  }
  return path;
}
