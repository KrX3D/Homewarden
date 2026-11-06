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
