import { createContext, useCallback, useContext, useMemo, type ReactNode } from 'react';
import en from './i18n/en.json';
import de from './i18n/de.json';
import { ApiError } from './api';

export type Lang = 'en' | 'de';
type Dict = Record<string, string>;
const dicts: Record<Lang, Dict> = { en, de };

export function detectLang(): Lang {
  return navigator.language?.toLowerCase().startsWith('de') ? 'de' : 'en';
}

type TFn = (key: string, vars?: Record<string, string | number>) => string;
const Ctx = createContext<{ lang: Lang; t: TFn }>({ lang: 'en', t: (k) => k });

export function I18nProvider({ lang, children }: { lang: Lang; children: ReactNode }) {
  const t = useCallback<TFn>(
    (key, vars) => {
      let s = dicts[lang][key] ?? dicts.en[key] ?? key;
      if (vars) for (const [k, v] of Object.entries(vars)) s = s.split(`{${k}}`).join(String(v));
      return s;
    },
    [lang],
  );
  const value = useMemo(() => ({ lang, t }), [lang, t]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export const useT = () => useContext(Ctx).t;
export const useLang = () => useContext(Ctx).lang;

/** Turns an API error code into a translated sentence. */
export function errorText(t: TFn, err: unknown): string {
  if (err instanceof ApiError) {
    const key = 'error.' + err.code;
    const s = t(key, (err.detail as Record<string, number>) ?? undefined);
    if (s !== key) return s;
    return err.message && err.message !== err.code ? `${t('error.generic')} (${err.message})` : t('error.generic');
  }
  return t('error.network');
}

export function formatTime(lang: Lang, unix: number | null | undefined): string {
  if (!unix) return '';
  return new Date(unix * 1000).toLocaleString(lang === 'de' ? 'de-DE' : 'en-GB', { dateStyle: 'medium', timeStyle: 'short' });
}

/** "3 min ago" style relative time. */
export function formatAgo(lang: Lang, unix: number | null | undefined): string {
  if (!unix) return '';
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' });
  const diff = unix - Date.now() / 1000;
  const abs = Math.abs(diff);
  if (abs < 60) return rtf.format(Math.round(diff), 'second');
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
  return rtf.format(Math.round(diff / 86400), 'day');
}
