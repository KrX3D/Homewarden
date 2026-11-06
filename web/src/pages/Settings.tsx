import { useEffect, useState, type FormEvent } from 'react';
import { NavLink, Navigate, Route, Routes } from 'react-router-dom';
import QRCode from 'qrcode';
import { del, get, post, put, type Role, type User } from '../api';
import { formatTime, useLang, useT } from '../i18n';
import { useSession } from '../session';
import { ConfirmButton, ErrorAlert, Modal, Segmented, useAction } from '../components/ui';
import { IconEdit, IconPlus } from '../components/icons';

export type SettingsTab = { path: string; label: string; perm?: string; element: JSX.Element };

/** Tabs contributed by other features (see extensions.ts). */
import { settingsTabs as extraSettingsTabs } from '../extensions';

export default function SettingsPage() {
  const t = useT();
  const { can } = useSession();
  const tabs: SettingsTab[] = [
    { path: 'account', label: t('settings.account'), element: <AccountTab /> },
    { path: 'users', label: t('settings.users'), perm: 'users.manage', element: <UsersTab /> },
    { path: 'roles', label: t('settings.roles'), perm: 'users.manage', element: <RolesTab /> },
    ...extraSettingsTabs.map((x) => ({ ...x, label: t(x.labelKey) })),
  ].filter((x) => !x.perm || can(x.perm));
  return (
    <>
      <div className="page-head"><h1>{t('settings.title')}</h1></div>
      <nav className="tabs">
        {tabs.map((x) => <NavLink key={x.path} to={x.path}>{x.label}</NavLink>)}
      </nav>
      <Routes>
        <Route index element={<Navigate to="account" replace />} />
        {tabs.map((x) => <Route key={x.path} path={x.path} element={x.element} />)}
        <Route path="*" element={<Navigate to="account" replace />} />
      </Routes>
    </>
  );
}

// ---------------------------------------------------------------- account

function AccountTab() {
  const t = useT();
  const { me, prefs, setPrefs, meta } = useSession();
  return (
    <div className="grid-2">
      <section className="card">
        <h2>{t('account.appearance')}</h2>
        <label className="field">
          {t('account.theme')}
          <Segmented
            label={t('account.theme')}
            value={prefs.theme ?? 'system'}
            onChange={(v) => setPrefs({ theme: v })}
            options={[
              { value: 'system', label: t('account.themeSystem') },
              { value: 'light', label: t('account.themeLight') },
              { value: 'dark', label: t('account.themeDark') },
            ]}
          />
        </label>
        <label className="field">
          {t('account.language')}
          <select id="pref-lang" value={prefs.lang ?? 'auto'} onChange={(e) => setPrefs({ lang: e.target.value as 'auto' | 'en' | 'de' })}>
            <option value="auto">{t('account.langAuto')}</option>
            <option value="en">English</option>
            <option value="de">Deutsch</option>
          </select>
        </label>
        <p className="muted small">
          {t('account.signedInAs', { user: me!.user.username, role: me!.role.name })} · Homewarden {meta?.build.version}
        </p>
      </section>
      <PasswordCard />
      <TOTPCard />
    </div>
  );
}

function PasswordCard() {
  const t = useT();
  const { meta } = useSession();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [done, setDone] = useState(false);
  const { busy, error, run } = useAction();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setDone(false);
    run(async () => {
      await put('/api/me/password', { current, new: next });
      setCurrent('');
      setNext('');
      setDone(true);
    });
  };
  return (
    <form className="card" onSubmit={submit}>
      <h2>{t('account.password')}</h2>
      <label className="field">
        {t('account.currentPassword')}
        <input id="pw-current" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
      </label>
      <label className="field">
        {t('account.newPassword')}
        <input id="pw-new" type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} minLength={meta?.minPassword} required />
        <span className="hint">{t('account.passwordOthers')}</span>
      </label>
      <ErrorAlert error={error} />
      {done && <div className="alert ok">{t('account.passwordChanged')}</div>}
      <div><button className="btn" type="submit" disabled={busy}>{t('account.changePassword')}</button></div>
    </form>
  );
}

function TOTPCard() {
  const t = useT();
  const { me, refresh } = useSession();
  const [pending, setPending] = useState<{ secret: string; url: string; qr: string } | null>(null);
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const { busy, error, run } = useAction();
  const enabled = me!.user.totpEnabled;

  const start = () =>
    run(async () => {
      const r = await post<{ secret: string; url: string }>('/api/me/totp/setup');
      const qr = await QRCode.toDataURL(r.url, { margin: 1, width: 180 });
      setPending({ ...r, qr });
    });
  const enable = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      await post('/api/me/totp/enable', { code });
      setPending(null);
      setCode('');
      await refresh();
    });
  };
  const disable = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      await post('/api/me/totp/disable', { password });
      setPassword('');
      await refresh();
    });
  };

  return (
    <section className="card">
      <div className="card-head">
        <h2 className="grow">{t('account.totp')}</h2>
        <span className={'pill dot ' + (enabled ? 'ok' : 'off')}>{enabled ? t('common.on') : t('common.off')}</span>
      </div>
      <p className="muted small">{t('account.totpIntro')}</p>
      {!enabled && !pending && <div><button className="btn ghost" type="button" onClick={start} disabled={busy}>{t('account.totpStart')}</button></div>}
      {!enabled && pending && (
        <form className="stack" onSubmit={enable}>
          <p className="small">{t('account.totpScan')}</p>
          <img className="qr" src={pending.qr} alt={t('account.totpQr')} />
          <p className="small muted">{t('account.totpManual')} <code>{pending.secret}</code></p>
          <label className="field">
            {t('account.totpCode')}
            <input id="totp-code" type="text" inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} maxLength={7} required />
          </label>
          <div className="row">
            <button className="btn" type="submit" disabled={busy}>{t('account.totpEnable')}</button>
            <button className="btn subtle" type="button" onClick={() => setPending(null)}>{t('common.cancel')}</button>
          </div>
        </form>
      )}
      {enabled && (
        <form className="stack" onSubmit={disable}>
          <label className="field">
            {t('account.totpDisablePw')}
            <input id="totp-disable-pw" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          </label>
          <div><button className="btn ghost" type="submit" disabled={busy}>{t('account.totpDisable')}</button></div>
        </form>
      )}
      <ErrorAlert error={error} />
    </section>
  );
}

// ---------------------------------------------------------------- users

function UsersTab() {
  const t = useT();
  const lang = useLang();
  const { me } = useSession();
  const [users, setUsers] = useState<User[]>([]);
  const [roles, setRoles] = useState<Role[]>([]);
  const [editing, setEditing] = useState<User | 'new' | null>(null);
  const { error, run } = useAction();

  const load = () => run(async () => {
    const [u, r] = await Promise.all([get<User[]>('/api/users'), get<Role[]>('/api/roles')]);
    setUsers(u);
    setRoles(r);
  });
  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const roleName = (id: number) => roles.find((r) => r.id === id)?.name ?? '?';

  return (
    <section className="stack">
      <div className="page-head">
        <p className="muted grow">{t('users.intro')}</p>
        <button className="btn" type="button" onClick={() => setEditing('new')}><IconPlus />{t('users.add')}</button>
      </div>
      <ErrorAlert error={error} />
      <div className="table-wrap">
        <table className="tbl">
          <thead><tr><th>{t('users.username')}</th><th>{t('users.role')}</th><th>{t('users.twoFactor')}</th><th>{t('users.status')}</th><th>{t('users.lastLogin')}</th><th></th></tr></thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td><strong>{u.username}</strong>{u.id === me!.user.id && <span className="muted small"> ({t('users.you')})</span>}</td>
                <td>{roleName(u.roleId)}</td>
                <td><span className={'pill ' + (u.totpEnabled ? 'ok' : 'off')}>{u.totpEnabled ? t('common.on') : t('common.off')}</span></td>
                <td><span className={'pill dot ' + (u.disabled ? 'crit' : 'ok')}>{u.disabled ? t('users.disabled') : t('users.active')}</span></td>
                <td className="small muted">{formatTime(lang, u.lastLogin) || t('users.never')}</td>
                <td style={{ textAlign: 'right' }}><button className="btn subtle sm" type="button" onClick={() => setEditing(u)} aria-label={t('common.edit')}><IconEdit /></button></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editing && <UserModal user={editing === 'new' ? null : editing} roles={roles} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
    </section>
  );
}

function UserModal({ user, roles, onClose, onSaved }: { user: User | null; roles: Role[]; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const { me, meta } = useSession();
  const [username, setUsername] = useState(user?.username ?? '');
  const [roleId, setRoleId] = useState<number>(user?.roleId ?? roles.find((r) => r.name === 'Viewer')?.id ?? roles[0]?.id);
  const [disabled, setDisabled] = useState(user?.disabled ?? false);
  const [password, setPassword] = useState('');
  const { busy, error, run } = useAction();
  const isSelf = user?.id === me!.user.id;

  const save = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      if (!user) await post('/api/users', { username, password, roleId });
      else await put(`/api/users/${user.id}`, { username, roleId, disabled, ...(password ? { password } : {}) });
      onSaved();
    });
  };

  return (
    <Modal title={user ? t('users.edit', { user: user.username }) : t('users.add')} onClose={onClose}>
      <form className="stack" onSubmit={save} id="user-form">
        <label className="field">
          {t('users.username')}
          <input id="user-username" type="text" value={username} onChange={(e) => setUsername(e.target.value)} required />
        </label>
        <label className="field">
          {t('users.role')}
          <select id="user-role" value={roleId} onChange={(e) => setRoleId(Number(e.target.value))}>
            {roles.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </select>
        </label>
        <label className="field">
          {user ? t('users.newPassword') : t('users.password')}
          <input id="user-password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={meta?.minPassword} required={!user} />
          <span className="hint">{user ? t('users.newPasswordHint') : t('common.passwordHint', { n: meta?.minPassword ?? 10 })}</span>
        </label>
        {user && !isSelf && (
          <label className="check"><input id="user-disabled" type="checkbox" checked={disabled} onChange={(e) => setDisabled(e.target.checked)} />{t('users.disableAccount')}</label>
        )}
        <ErrorAlert error={error} />
      </form>
      <div className="modal-foot">
        {user && user.totpEnabled && !isSelf && (
          <ConfirmButton label={t('users.reset2fa')} onConfirm={() => run(async () => { await post(`/api/users/${user.id}/reset-totp`); onSaved(); })} />
        )}
        {user && !isSelf && (
          <ConfirmButton label={t('common.delete')} onConfirm={() => run(async () => { await del(`/api/users/${user.id}`); onSaved(); })} />
        )}
        <span style={{ flex: 1 }} />
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button className="btn" type="submit" form="user-form" disabled={busy}>{t('common.save')}</button>
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------- roles

function RolesTab() {
  const t = useT();
  const [roles, setRoles] = useState<Role[]>([]);
  const [editing, setEditing] = useState<Role | 'new' | null>(null);
  const { error, run } = useAction();
  const load = () => run(async () => setRoles(await get<Role[]>('/api/roles')));
  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <section className="stack">
      <div className="page-head">
        <p className="muted grow">{t('roles.intro')}</p>
        <button className="btn" type="button" onClick={() => setEditing('new')}><IconPlus />{t('roles.add')}</button>
      </div>
      <ErrorAlert error={error} />
      <div className="grid-2">
        {roles.map((r) => (
          <div className="card" key={r.id}>
            <div className="card-head">
              <h2 className="grow">{r.name}</h2>
              {r.builtin ? <span className="pill off">{t('roles.builtin')}</span> : <button className="btn subtle sm" type="button" onClick={() => setEditing(r)} aria-label={t('common.edit')}><IconEdit /></button>}
            </div>
            <div className="row" style={{ gap: 6 }}>
              {r.permissions.includes('*') ? <span className="chip">{t('perm.*')}</span> : r.permissions.map((p) => <span className="chip" key={p}>{t('perm.' + p)}</span>)}
            </div>
            {r.groups?.length > 0 && <span className="small muted">{t('roles.onlyGroups', { groups: r.groups.join(', ') })}</span>}
          </div>
        ))}
      </div>
      {editing && <RoleModal role={editing === 'new' ? null : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
    </section>
  );
}

function RoleModal({ role, onClose, onSaved }: { role: Role | null; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const { meta } = useSession();
  const [name, setName] = useState(role?.name ?? '');
  const [perms, setPerms] = useState<string[]>(role?.permissions ?? ['devices.view']);
  const [groups, setGroups] = useState<string[]>(role?.groups ?? []);
  const [allGroups, setAllGroups] = useState<string[]>([]);
  useEffect(() => { get<{ groupName: string }[]>('/api/devices?status=all').then((d) => setAllGroups([...new Set(d.map((x) => x.groupName).filter(Boolean))].sort())).catch(() => {}); }, []);
  const { busy, error, run } = useAction();
  const toggle = (p: string) => setPerms((cur) => (cur.includes(p) ? cur.filter((x) => x !== p) : [...cur, p]));
  const save = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      if (role) await put(`/api/roles/${role.id}`, { name, permissions: perms, groups });
      else await post('/api/roles', { name, permissions: perms, groups });
      onSaved();
    });
  };
  return (
    <Modal title={role ? t('roles.edit') : t('roles.add')} onClose={onClose}>
      <form className="stack" id="role-form" onSubmit={save}>
        <label className="field">
          {t('roles.name')}
          <input id="role-name" type="text" value={name} onChange={(e) => setName(e.target.value)} required maxLength={64} />
        </label>
        <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0, gap: 8 }}>
          <legend className="small" style={{ fontWeight: 500, marginBottom: 6 }}>{t('roles.permissions')}</legend>
          {(meta?.permissions ?? []).map((p) => (
            <label className="check" key={p}>
              <input type="checkbox" checked={perms.includes(p)} onChange={() => toggle(p)} />
              <span>{t('perm.' + p)}<span className="muted small"> · {t('permDesc.' + p)}</span></span>
            </label>
          ))}
        </fieldset>
        <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0, gap: 8 }}>
          <legend className="small" style={{ fontWeight: 500, marginBottom: 6 }}>{t('roles.groups')}</legend>
          <span className="small muted">{t('roles.groupsHint')}</span>
          {allGroups.length === 0 && <span className="small muted">{t('roles.noGroups')}</span>}
          {allGroups.map((g) => (
            <label className="check" key={g}><input type="checkbox" checked={groups.includes(g)} onChange={() => setGroups((cur) => cur.includes(g) ? cur.filter((x) => x !== g) : [...cur, g])} />{g}</label>
          ))}
        </fieldset>
        <ErrorAlert error={error} />
      </form>
      <div className="modal-foot">
        {role && <ConfirmButton label={t('common.delete')} onConfirm={() => run(async () => { await del(`/api/roles/${role.id}`); onSaved(); })} />}
        <span style={{ flex: 1 }} />
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button className="btn" type="submit" form="role-form" disabled={busy}>{t('common.save')}</button>
      </div>
    </Modal>
  );
}
