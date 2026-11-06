import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';
import { ApiError, get, put, type Me, type Meta, type Prefs } from './api';
import { detectLang, I18nProvider, type Lang } from './i18n';

type Session = {
  meta: Meta | null;
  me: Me | null;
  loading: boolean;
  refresh: () => Promise<void>;
  can: (perm: string) => boolean;
  prefs: Prefs;
  setPrefs: (p: Partial<Prefs>) => Promise<void>;
};

const Ctx = createContext<Session>(null as unknown as Session);
export const useSession = () => useContext(Ctx);

function readLocal(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function writeLocal(key: string, v: string) {
  try {
    localStorage.setItem(key, v);
  } catch {
    /* storage blocked */
  }
}

function applyTheme(theme: Prefs['theme']) {
  const root = document.documentElement;
  if (theme === 'light' || theme === 'dark') root.dataset.theme = theme;
  else delete root.dataset.theme;
  writeLocal('hw.theme', theme ?? 'system');
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [meta, setMeta] = useState<Meta | null>(null);
  const [me, setMe] = useState<Me | null>(null);
  const [loading, setLoading] = useState(true);
  // Before login, preferences come from this browser; after login, from the account.
  const [localPrefs, setLocalPrefs] = useState<Prefs>(() => ({
    theme: (readLocal('hw.theme') as Prefs['theme']) ?? 'system',
    lang: (readLocal('hw.lang') as Prefs['lang']) ?? 'auto',
  }));

  const refresh = useCallback(async () => {
    try {
      const m = await get<Meta>('/api/meta');
      setMeta(m);
      if (m.needsSetup) {
        setMe(null);
        return;
      }
      try {
        setMe(await get<Me>('/api/me'));
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) setMe(null);
        else throw e;
      }
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh().catch(() => setLoading(false));
  }, [refresh]);

  const prefs: Prefs = { ...localPrefs, ...(me?.user.prefs ?? {}) };

  useEffect(() => applyTheme(prefs.theme), [prefs.theme]);

  const setPrefs = useCallback(
    async (p: Partial<Prefs>) => {
      const next = { ...prefs, ...p };
      setLocalPrefs(next);
      if (next.lang) writeLocal('hw.lang', next.lang);
      if (me) {
        const saved = await put<Prefs>('/api/me/prefs', next);
        setMe({ ...me, user: { ...me.user, prefs: saved } });
      }
    },
    [prefs, me],
  );

  const can = useCallback((perm: string) => !!me && (me.role.permissions.includes('*') || me.role.permissions.includes(perm)), [me]);
  const lang: Lang = prefs.lang === 'en' || prefs.lang === 'de' ? prefs.lang : detectLang();

  return (
    <Ctx.Provider value={{ meta, me, loading, refresh, can, prefs, setPrefs }}>
      <I18nProvider lang={lang}>{children}</I18nProvider>
    </Ctx.Provider>
  );
}
