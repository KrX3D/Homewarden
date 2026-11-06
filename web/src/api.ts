// Thin wrapper around fetch for the Homewarden API.

export class ApiError extends Error {
  constructor(public status: number, public code: string, public detail?: unknown, message?: string) {
    super(message || code);
  }
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      // Required by the server on every state-changing request (CSRF protection).
      'X-Homewarden': '1',
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data: any = null;
  try {
    data = await res.json();
  } catch {
    /* empty body */
  }
  if (!res.ok) throw new ApiError(res.status, data?.error ?? 'internal', data?.detail, data?.message);
  return data as T;
}

export const get = <T,>(p: string) => api<T>('GET', p);
export const post = <T,>(p: string, b?: unknown) => api<T>('POST', p, b ?? {});
export const put = <T,>(p: string, b?: unknown) => api<T>('PUT', p, b ?? {});
export const del = <T,>(p: string) => api<T>('DELETE', p);

export type Prefs = {
  theme?: 'system' | 'light' | 'dark';
  lang?: 'auto' | 'en' | 'de';
  deviceView?: 'cards' | 'table' | 'tree';
};

export type User = {
  id: number;
  username: string;
  roleId: number;
  totpEnabled: boolean;
  disabled: boolean;
  prefs: Prefs;
  createdAt: number;
  lastLogin: number | null;
};

export type Role = { id: number; name: string; builtin: boolean; permissions: string[] };

export type Meta = {
  build: { version: string; commit: string; buildDate: string };
  needsSetup: boolean;
  minPassword: number;
  permissions: string[];
};

export type Me = { user: User; role: Role };

export type ProbeMethod = 'none' | 'ping' | 'tcp' | 'http';
export type Probe = { method: ProbeMethod; port?: number; url?: string };
export type PresenceConfig = { reach: Probe; ready: Probe; settleSeconds: number; intervalSeconds: number };
export type DeviceState = 'unknown' | 'offline' | 'reachable' | 'ready' | 'paused';
export type DeviceStatus = 'active' | 'discovered' | 'ignored';

export type Device = {
  id: number;
  name: string;
  kind: string;
  host: string;
  mac: string;
  parentId: number | null;
  groupName: string;
  status: DeviceStatus;
  source: string;
  externalId: string;
  presence: PresenceConfig;
  config: Record<string, unknown>;
  state: DeviceState;
  stateSince: number | null;
  lastSeen: number | null;
  rttMs: number | null;
  notes: string;
  createdAt: number;
};

export type CredentialType = 'ssh_key' | 'password' | 'token';
export type Credential = { id: number; name: string; type: CredentialType; username: string; public: string; createdAt: number; updatedAt: number; usedBy: number };
export type ConnectionSettings = { method: 'ssh' | ''; port?: number; credentialId?: number; user?: string; sudo: boolean };
export type Facts = { user: string; hostname: string; os: string; kernel: string; arch: string; sudo: boolean; hostKey: string; hostKeyFirst: boolean };

export type Text = Record<string, string>;
export type IntegrationSetting = {
  key: string; type: 'string' | 'number' | 'bool' | 'select' | 'credential' | 'password';
  label: Text; help?: Text; default?: unknown; required?: boolean; options?: { value: string; label: Text }[];
  credentialTypes?: string[]; placeholder?: string;
};
export type Permissions = { ssh: boolean; docker: boolean; httpDevice: boolean; httpHosts: string[] | null };
export type Integration = {
  id: string; version: string; name: Text; description: Text; howto?: Text; connection: string;
  permissions: Permissions; settings?: IntegrationSetting[]; presence?: PresenceConfig; capabilities: string[];
  source: string; iconSvg?: string;
};
export type CatalogEntry = { source: string; id: string; version: string; name: Text; description: Text; permissions: Permissions; iconSvg?: string };
export type Component = {
  deviceId: number; id: string; name: string; group: string; installed: string; available: string;
  updateAvailable: boolean; security: boolean; updatable: boolean; meta: Record<string, any>; updatedAt: number;
};
export type JobStatus = 'queued' | 'running' | 'success' | 'failed' | 'blocked' | 'cancelled';
export type Job = {
  id: number; type: string; deviceId: number | null; deviceName: string; title: string; params: any; status: JobStatus;
  createdBy: string; createdAt: number; startedAt: number | null; finishedAt: number | null; log?: string; result: any;
};
export type VersionChange = { componentId: string; name: string; oldVersion: string; newVersion: string; at: number };
export type Software = {
  components: Component[]; facts: { facts: Record<string, any>; scannedAt: number; error: string };
  history: VersionChange[]; jobs: Job[];
};

export type BackupTargetConfig = { host?: string; share?: string; credentialId?: number; domain?: string; folder?: string };
export type BackupTarget = { id: number; name: string; type: 'smb' | 'local'; config: BackupTargetConfig; createdAt: number };
export type Backup = { id: number; deviceId: number; targetId: number | null; name: string; path: string; size: number; at: number; jobId: number | null };
export type BackupSettings = { targetId: number; beforeUpdate: boolean; keep: number; schedule?: string };
export type Guard = { name: string; type: 'ssh' | 'http'; command?: string; url?: string; contains?: string; mode: 'block' | 'warn' };

export type Policy = { mode: 'manual' | 'auto' | 'scheduled'; schedule: string; scope: 'security' | 'all' };
export type APIToken = { id: number; name: string; createdAt: number; lastUsed: number | null };
export type MQTTSettings = { enabled: boolean; broker: string; credentialId?: number; baseTopic: string };
