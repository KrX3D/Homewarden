// Single sign-on settings (OIDC: Authentik, Authelia, Keycloak, ...).
import { useEffect, useState, type FormEvent } from 'react';
import { get, put, type Role } from '../api';
import { useT } from '../i18n';
import { ErrorAlert, useAction } from '../components/ui';
import { IconPlus, IconTrash } from '../components/icons';
import { settingsTabs } from '../extensions';

type Settings = {
  enabled: boolean; issuer: string; clientId: string; clientSecret?: string; label: string;
  usernameClaim: string; groupsClaim: string; roleMap: { group: string; roleId: number }[]; defaultRoleId: number; autoCreate: boolean;
};

function SSOTab() {
  const t = useT();
  const [s, setS] = useState<Settings | null>(null);
  const [hasSecret, setHasSecret] = useState(false);
  const [redirect, setRedirect] = useState('');
  const [roles, setRoles] = useState<Role[]>([]);
  const [ok, setOk] = useState(false);
  const { busy, error, run } = useAction();
  useEffect(() => {
    get<{ settings: Settings; hasSecret: boolean; redirectUrl: string }>('/api/settings/oidc').then((r) => { setS({ ...r.settings, roleMap: r.settings.roleMap ?? [] }); setHasSecret(r.hasSecret); setRedirect(r.redirectUrl); }).catch(() => {});
    get<Role[]>('/api/roles').then(setRoles).catch(() => {});
  }, []);
  if (!s) return null;
  const set = (p: Partial<Settings>) => { setOk(false); setS({ ...s, ...p }); };
  const save = (e: FormEvent) => { e.preventDefault(); run(async () => { await put('/api/settings/oidc', s); setOk(true); if (s.clientSecret) setHasSecret(true); }); };
  return (
    <form className="card" style={{ maxWidth: 720 }} onSubmit={save}>
      <h2>{t('sso.title')}</h2>
      <p className="small muted">{t('sso.intro')}</p>
      <div className="stack small" style={{ gap: 4 }}><span>{t('sso.redirect')}</span><code style={{ overflowWrap: 'anywhere' }}>{redirect}</code></div>
      <label className="check"><input id="sso-enabled" type="checkbox" checked={s.enabled} onChange={(e) => set({ enabled: e.target.checked })} />{t('sso.enabled')}</label>
      <label className="field">{t('sso.issuer')}<input id="sso-issuer" type="url" value={s.issuer} onChange={(e) => set({ issuer: e.target.value })} placeholder="https://auth.krx3d.de/application/o/homewarden/" />
        <span className="hint">{t('sso.issuerHint')}</span></label>
      <div className="row">
        <label className="field" style={{ flex: 1 }}>{t('sso.clientId')}<input id="sso-client" type="text" value={s.clientId} onChange={(e) => set({ clientId: e.target.value })} /></label>
        <label className="field" style={{ flex: 1 }}>{t('sso.clientSecret')}<input id="sso-secret" type="password" value={s.clientSecret ?? ''} placeholder={hasSecret ? t('notify.unchanged') : ''} onChange={(e) => set({ clientSecret: e.target.value })} /></label>
      </div>
      <div className="row">
        <label className="field" style={{ flex: 1 }}>{t('sso.label')}<input type="text" value={s.label} onChange={(e) => set({ label: e.target.value })} placeholder="Authentik" /></label>
        <label className="field" style={{ flex: 1 }}>{t('sso.usernameClaim')}<input type="text" className="mono" value={s.usernameClaim} onChange={(e) => set({ usernameClaim: e.target.value })} /></label>
        <label className="field" style={{ flex: 1 }}>{t('sso.groupsClaim')}<input type="text" className="mono" value={s.groupsClaim} onChange={(e) => set({ groupsClaim: e.target.value })} /></label>
      </div>
      <div className="stack" style={{ gap: 6 }}>
        <strong className="small">{t('sso.roleMap')}</strong>
        {s.roleMap.map((m, i) => (
          <div className="row" key={i}>
            <input type="text" value={m.group} placeholder={t('sso.group')} onChange={(e) => set({ roleMap: s.roleMap.map((x, j) => (j === i ? { ...x, group: e.target.value } : x)) })} style={{ flex: 1 }} />
            <span>→</span>
            <select value={m.roleId} onChange={(e) => set({ roleMap: s.roleMap.map((x, j) => (j === i ? { ...x, roleId: Number(e.target.value) } : x)) })} style={{ flex: 1 }}>
              {roles.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
            </select>
            <button className="btn subtle" type="button" aria-label={t('common.delete')} onClick={() => set({ roleMap: s.roleMap.filter((_, j) => j !== i) })}><IconTrash /></button>
          </div>
        ))}
        <div><button className="btn subtle sm" type="button" onClick={() => set({ roleMap: [...s.roleMap, { group: '', roleId: roles.find((r) => r.name === 'Viewer')?.id ?? roles[0]?.id }] })}><IconPlus />{t('sso.addMap')}</button></div>
      </div>
      <label className="field">{t('sso.defaultRole')}
        <select value={s.defaultRoleId} onChange={(e) => set({ defaultRoleId: Number(e.target.value) })}>
          <option value={0}>{t('sso.deny')}</option>
          {roles.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select>
      </label>
      <label className="check"><input type="checkbox" checked={s.autoCreate} onChange={(e) => set({ autoCreate: e.target.checked })} />{t('sso.autoCreate')}</label>
      {ok && <div className="alert ok">{t('conn.saved')}</div>}
      <ErrorAlert error={error} />
      <div><button className="btn" type="submit" disabled={busy}>{t('common.save')}</button></div>
    </form>
  );
}

settingsTabs.push({ path: 'sso', labelKey: 'settings.sso', perm: 'settings.manage', element: <SSOTab /> });
