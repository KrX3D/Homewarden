import { useEffect, useState, type ReactNode } from 'react';
import { Navigate, NavLink, Route, Routes, useLocation } from 'react-router-dom';
import { useSession } from './session';
import { useT } from './i18n';
import { post } from './api';
import { Spinner } from './components/ui';
import { IconDevices, IconLogout, IconMenu, IconSettings, Logo } from './components/icons';
import SetupPage from './pages/Setup';
import LoginPage from './pages/Login';
import DevicesPage from './pages/Devices';
import SettingsPage from './pages/Settings';

export type NavItem = { to: string; label: string; icon: ReactNode; perm?: string; badge?: number };

export default function App() {
  const { meta, me, loading } = useSession();
  if (loading) {
    return <div className="auth-wrap"><Spinner /></div>;
  }
  if (meta?.needsSetup) return <SetupPage />;
  if (!me) return <LoginPage />;
  return <Shell />;
}

function Shell() {
  const t = useT();
  const { me, can, refresh } = useSession();
  const [open, setOpen] = useState(false);
  const loc = useLocation();
  useEffect(() => setOpen(false), [loc.pathname]);

  const nav: NavItem[] = [
    { to: '/devices', label: t('nav.devices'), icon: <IconDevices />, perm: 'devices.view' },
    { to: '/settings', label: t('nav.settings'), icon: <IconSettings /> },
  ];

  const logout = async () => {
    await post('/api/auth/logout');
    await refresh();
  };

  return (
    <div className="shell">
      <aside className={'sidebar' + (open ? ' open' : '')} aria-label={t('nav.label')}>
        <NavLink to="/" className="brand"><Logo />Homewarden</NavLink>
        <nav className="nav">
          {nav.filter((n) => !n.perm || can(n.perm)).map((n) => (
            <NavLink key={n.to} to={n.to}>
              {n.icon}
              {n.label}
              {!!n.badge && <span className="nav-badge">{n.badge}</span>}
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-foot">
          <div className="small">
            <div style={{ fontWeight: 600 }}>{me!.user.username}</div>
            <div className="muted">{me!.role.name}</div>
          </div>
          <button type="button" className="btn subtle sm" style={{ justifyContent: 'flex-start' }} onClick={logout}>
            <IconLogout />{t('nav.logout')}
          </button>
        </div>
      </aside>
      <div style={{ minWidth: 0 }}>
        <div className="mobile-bar">
          <button type="button" className="btn subtle" aria-label={t('nav.menu')} onClick={() => setOpen((o) => !o)}><IconMenu /></button>
          <span className="brand" style={{ padding: 0 }}><Logo />Homewarden</span>
        </div>
        <main className="main">
          <Routes>
            <Route path="/" element={<Navigate to={can('devices.view') ? '/devices' : '/settings'} replace />} />
            <Route path="/devices/*" element={<DevicesPage />} />
            <Route path="/settings/*" element={<SettingsPage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </main>
      </div>
    </div>
  );
}
