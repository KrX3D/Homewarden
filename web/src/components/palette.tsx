// Command palette (Ctrl+K / Cmd+K): jump to pages, settings, devices, monitors and apps.
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { get, type Device, type Monitor } from '../api';
import { useT } from '../i18n';
import { useSession } from '../session';
import { settingsTabs } from '../extensions';
import { IconDevices, IconPulse, IconSearch, IconSettings } from './icons';

export type PaletteNav = { to: string; label: string; icon: ReactNode; perm?: string };

type Item = { group: string; label: string; hint?: string; icon?: ReactNode; to?: string; href?: string };

export function usePaletteHotkey(open: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        open();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open]);
}

export function CommandPalette({ nav, onClose }: { nav: PaletteNav[]; onClose: () => void }) {
  const t = useT();
  const navigate = useNavigate();
  const { can } = useSession();
  const [q, setQ] = useState('');
  const [sel, setSel] = useState(0);
  const [devices, setDevices] = useState<Device[]>([]);
  const [monitors, setMonitors] = useState<Monitor[]>([]);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    input.current?.focus();
    if (can('devices.view')) {
      get<Device[]>('/api/devices?status=active').then(setDevices).catch(() => {});
      get<Monitor[]>('/api/monitors').then(setMonitors).catch(() => {});
    }
  }, [can]);

  const items = useMemo(() => {
    const all: Item[] = [
      ...nav.filter((n) => !n.perm || can(n.perm)).map((n) => ({ group: t('palette.pages'), label: n.label, icon: n.icon, to: n.to })),
      { group: t('palette.settings'), label: t('settings.account'), icon: <IconSettings />, to: '/settings/account' },
      ...settingsTabs.filter((s) => !s.perm || can(s.perm)).map((s) => ({ group: t('palette.settings'), label: t(s.labelKey), icon: <IconSettings />, to: '/settings/' + s.path })),
      ...devices.map((d) => ({ group: t('nav.devices'), label: d.name, hint: [d.host, d.kind].filter(Boolean).join(' · '), icon: <IconDevices />, to: '/devices/' + d.id })),
      ...monitors.map((m) => ({ group: t('nav.monitors'), label: m.name, hint: t('mon.status.' + m.status), icon: <IconPulse />, to: '/monitors' })),
      ...monitors.filter((m) => m.type === 'http' && m.config.url).map((m) => ({ group: t('palette.open'), label: m.name, hint: m.config.url, href: m.config.url })),
    ];
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    const hit = (i: Item) => words.every((w) => (i.label + ' ' + (i.hint ?? '') + ' ' + i.group).toLowerCase().includes(w));
    return (words.length ? all.filter(hit) : all.filter((i) => i.group === t('palette.pages') || i.group === t('palette.settings'))).slice(0, 60);
  }, [q, nav, devices, monitors, can, t]);

  useEffect(() => setSel(0), [q]);
  useEffect(() => { list.current?.querySelector('.pal-item.sel')?.scrollIntoView({ block: 'nearest' }); }, [sel]);

  const go = (i: Item | undefined) => {
    if (!i) return;
    onClose();
    if (i.href) window.open(i.href, '_blank', 'noopener');
    else if (i.to) navigate(i.to);
  };
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setSel((s) => Math.min(items.length - 1, s + 1)); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setSel((s) => Math.max(0, s - 1)); }
    else if (e.key === 'Enter') { e.preventDefault(); go(items[sel]); }
    else if (e.key === 'Escape') onClose();
  };

  let last = '';
  return (
    <div className="modal-back pal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="pal" role="dialog" aria-modal="true" aria-label={t('palette.title')}>
        <div className="pal-search"><IconSearch /><input ref={input} id="palette-input" value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={onKey} placeholder={t('palette.placeholder')} autoComplete="off" /></div>
        <div className="pal-list" ref={list}>
          {items.length === 0 && <div className="pal-empty">{t('palette.none')}</div>}
          {items.map((i, n) => {
            const head = i.group !== last ? (last = i.group) : null;
            return (
              <div key={n}>
                {head && <div className="pal-group">{head}</div>}
                <button type="button" className={'pal-item' + (n === sel ? ' sel' : '')} onMouseEnter={() => setSel(n)} onClick={() => go(i)}>
                  <span className="pal-ic">{i.icon ?? '↗'}</span>
                  <span className="pal-label">{i.label}</span>
                  {i.hint && <span className="pal-hint">{i.hint}</span>}
                </button>
              </div>
            );
          })}
        </div>
        <div className="pal-foot"><span><kbd>↑</kbd><kbd>↓</kbd> {t('palette.move')}</span><span><kbd>Enter</kbd> {t('palette.go')}</span><span><kbd>Esc</kbd> {t('common.close')}</span></div>
      </div>
    </div>
  );
}
