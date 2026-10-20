export interface Settings {
  aggressiveCacheReclamation: 'auto' | 'on' | 'off';
  storageDir: string; maxCacheBytes: number; videoStorageMode: 'none' | 'limited' | 'unlimited'; requestRetentionDays: number; throughputIntervalMinutes: number;
  prepareAheadSeconds: number; downloadSoftLimit: number; downloadGrowIntervalMS: number; localProtectionGraceSeconds: number; downloadUpstream: string; upstreamMode: string;
  proxyBackend: string; socks5Address: string; socks5Username: string; socks5Password?: string;
  autoStartCDN: boolean; autoStartQueue: boolean; manualLogDir: boolean; logDir: string;
}
export interface Job {
  running?: boolean; scanOnly?: boolean; phase?: string; error?: string; total?: number; checked?: number;
  hits?: number; downloaded?: number; missing?: number; reused?: number; failed?: number; current?: string;
  updated?: string; budgetReached?: boolean; catalogWarning?: string; failures?: { id: number; name: string; error: string }[];
}
export interface ImportJob extends Job {
  id?: number; imported?: number; skipped?: number; deleted?: number; cleanupAvailable?: boolean;
}
export interface Application {
  id: string; phase: string; error?: string; configSaved?: boolean; importStarted?: boolean; importSource?: string;
  vrcx?: {state:string;message:string};
}
export interface HealthResult {
  operation: string; route: string; mode: string; entry: string; state: string; channelID?: string; ip?: string;
  reason?: string; localDiagnostic?: string; localObservedAt?: string; estimatedLatencyMS?: number; estimatedSpeedBPS?: number;
  observedAt?: string; validUntil?: string; catalogTime?: string; sampleSongID?: number; http?: number;
  throughputSongID?: number; throughputObservedAt?: string; transferDurationMS?: number; transferredBytes?: number;
  sources?: {api: string; node: string; apiChannelID?: string}[];
  lastThroughput?: {bytes: number; durationNs: number; songID: number; observedAt: string};
}
export interface Health {
  closed?: boolean; checking?: boolean; manual?: boolean; checkKind?: string; resourcesPaused?: boolean;
  results?: HealthResult[]; finished?: string; nextCheck?: string; nextThroughput?: string;
  throughputStatus?: string; throughputSaveFailed?: boolean; policy?: { throughputInterval?: number };
}
export interface Status {
  running: boolean; settings: Settings; activeSettings: Settings; restartRequired: boolean;
  hosts: {ready: boolean; needsMigration?: boolean; conflict?: boolean; message: string}; portOK: boolean; httpsPortOK: boolean;
  portOwner?: {name: string; pid: number}; httpsPortOwner?: {name: string; pid: number}; cdnError?: string;
  storageError?: string; actionErrors?: Record<string,string>; batch: Job; lastBatch: Job; import: ImportJob;
  queue: {running?: boolean; desired?: boolean; current?: number; active?: number[]; songs?: {songId: number}[]; titles?: Record<string,string>; file?: string; completed?: number; logError?: string; error?: string};
  traffic?: {hits?: number; hitRate?: number; savedBytes?: number; playbackTransfers?: number; requests?: number; misses?: number; coldBodyMS?: number; coldBodySamples?: number; localBodyMS?: number; localBodySamples?: number; error?: string};
  socks5PasswordSet?: boolean; socks5CredentialError?: string;
  proxyNodes?: {saved?: number; loaded?: number; active?: number; error?: string; activeError?: string};
  activation?: {phase?: string; error?: string; started?: string; readyAt?: string; firstRequest?: string};
  defaultLogDir: string; defaultStorageDir: string; portableDir: string; upstreamMonitor?: Health;
  setupSettingsVersion: string; setupSettingsError?: string; setupApplication?: Application;
  setupNetwork?: {running?:boolean; error?:string; monitor?:Health};
}
export interface Inventory {
  updated?: string; coverageKnown?: boolean; totalSongs: number; coveredSongs: number; scanning?: boolean; error?: string;
  catalogRevision?: string; catalog?: {checkedAt?: string; revision?: string; error?: string; message?: string};
}
export interface Song { id: string; title?: string; current?: boolean }
export interface CacheEntry {
  key: string; stamp: string; bytes: number; songs: Song[]; songCount: number; known: boolean; protected: boolean; state: string; lastRequest: number;
}
export interface CachePage {storageID: string; entries: CacheEntry[]; total: number; totalBytes?: number; fileCount: number}
export interface RequestRecord {
  id: number; at: number; resource: string; method: string; range?: string; elapsedMS: number; bytes: number; status: number;
  cache: string; outcome: string; songs?: Song[]; moreSongs?: boolean;
}
export interface Download {
  id: number; resource: string; songs?: Song[]; moreSongs?: boolean; host: string; stage: string; size: number; bytes: number; bytesPerSecond: number; idleMS: number;
}
export const dated = (value?: string) => !!value && !value.startsWith('0001');
export const date = (value?: string | number) => typeof value === 'number' || dated(value) ? new Date(value!).toLocaleString() : '—';
export const gib = (value = 0) => (value / 1073741824).toFixed(3) + ' GiB';
export const mb = (value = 0) => (value / 1e6).toFixed(3) + ' MB';
export const active = (s?: Status) => ['checking', 'starting', 'hosts'].includes(s?.activation?.phase || '');
export const accelerationLabel = (s: Status) => s.running && s.hosts.ready ? '加速运行中' : s.running ? '加速未就绪' : '加速未运行';
