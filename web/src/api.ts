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
