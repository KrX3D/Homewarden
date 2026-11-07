// Integration-wide setup: settings and an SSH connection shared by every device of
// one integration. Devices only set what differs.
import { useEffect, useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { get, put, type Credential, type Integration, type IntegrationSetting, type Text } from '../api';
import { useLang, useT, type Lang } from '../i18n';
import { useSession } from '../session';
import { ErrorAlert, Modal, Spinner, useAction } from '../components/ui';
import { IconPlus } from '../components/icons';
import { CredentialModal } from './credentials';

const tx = (text: Text | undefined, lang: Lang) => (text ? text[lang] || text.en || '' : '');

export type DefaultsConnection = { credentialId?: number; user?: string; port?: number; sudo?: boolean };
export type IntegrationDefaults = {
  settings: Record<string, unknown>;
  connection: DefaultsConnection;
  devices: { id: number; name: string; own: string[]; ownConnection: boolean }[];
};

/** Loads an integration's shared settings; null while loading or without permission. */
export function useIntegrationDefaults(id: string | undefined, enabled = true) {
  const [def, setDef] = useState<IntegrationDefaults | null>(null);
  useEffect(() => {
    setDef(null);
    if (!id || id === 'generic' || !enabled) return;
    get<IntegrationDefaults>(`/api/integrations/${id}/defaults`).then(setDef).catch(() => {});
  }, [id, enabled]);
  return def;
}

const unset = (v: unknown) => v === undefined || v === null || v === '';

/** Drops empty values so they fall back to the integration defaults. */
export function cleanSettings(v: Record<string, unknown>) {
  return Object.fromEntries(Object.entries(v).filter(([, x]) => !unset(x)));
}

/**
 * One manifest setting. With `inherited` set, leaving the field empty uses that value
 * (from the integration setup or the manifest default) and the field says so.
 */
export function SettingField({ s, value, onChange, creds, inherited, idPrefix = 'set-' }: {
  s: IntegrationSetting; value: unknown; onChange: (v: unknown) => void; creds: Credential[]; inherited?: unknown; idPrefix?: string;
}) {
  const lang = useLang();
  const t = useT();
  const id = idPrefix + s.key;
  const help = tx(s.help, lang);
  const hasInherited = !unset(inherited);
  const credName = (v: unknown) => creds.find((c) => c.id === Number(v))?.name ?? `#${v}`;
  const optLabel = (v: unknown) => tx(s.options?.find((o) => o.value === String(v))?.label, lang) || String(v);
  const shown = (v: unknown) => (s.type === 'credential' ? credName(v) : s.type === 'select' ? optLabel(v) : s.type === 'bool' ? (v ? t('common.on') : t('common.off')) : s.type === 'password' ? '••••••' : String(v));
  const defaultOpt = hasInherited ? <option value="">{t('integ.useDefault', { v: shown(inherited) })}</option> : null;

  if (s.type === 'bool') {
    if (hasInherited) {
      return (
        <label className="field">
          {tx(s.label, lang)}
          <select id={id} value={unset(value) ? '' : value ? 'true' : 'false'} onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value === 'true')}>
            {defaultOpt}
            <option value="true">{t('common.on')}</option>
            <option value="false">{t('common.off')}</option>
          </select>
          {help && <span className="hint">{help}</span>}
        </label>
      );
    }
    return (
      <div className="stack" style={{ gap: 2 }}>
        <label className="check"><input id={id} type="checkbox" checked={!!value} onChange={(e) => onChange(e.target.checked)} />{tx(s.label, lang)}</label>
        {help && <span className="small muted" style={{ paddingLeft: 24 }}>{help}</span>}
      </div>
    );
  }
  return (
    <label className="field">
      {tx(s.label, lang)}
      {s.type === 'select' ? (
        <select id={id} value={String(value ?? '')} onChange={(e) => onChange(e.target.value || undefined)}>
          {defaultOpt}
          {s.options?.map((o) => <option key={o.value} value={o.value}>{tx(o.label, lang)}</option>)}
        </select>
      ) : s.type === 'credential' ? (
        <select id={id} value={String(value ?? '')} onChange={(e) => onChange(e.target.value ? Number(e.target.value) : undefined)}>
          {defaultOpt ?? <option value="">{t('conn.pickCredential')}</option>}
          {creds.filter((c) => !s.credentialTypes?.length || s.credentialTypes.includes(c.type)).map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
        </select>
      ) : (
        <input id={id} type={s.type === 'number' ? 'number' : s.type === 'password' ? 'password' : 'text'} value={String(value ?? '')}
          placeholder={hasInherited ? t('integ.defaultPlaceholder', { v: shown(inherited) }) : s.placeholder}
          onChange={(e) => onChange(s.type === 'number' ? (e.target.value === '' ? undefined : Number(e.target.value)) : e.target.value)}
          required={s.required && !hasInherited} />
      )}
      {help && <span className="hint">{help}</span>}
    </label>
  );
}

/** The setup page of one integration, opened from the Integrations page. */
export function IntegrationSetupModal({ integ, onClose }: { integ: Integration; onClose: () => void }) {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const def = useIntegrationDefaults(integ.id);
  const [creds, setCreds] = useState<Credential[]>([]);
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [conn, setConn] = useState<DefaultsConnection>({ port: 22, sudo: false });
  const [creating, setCreating] = useState<null | { types: Credential['type'][]; apply: (c: Credential) => void }>(null);
  const [saved, setSaved] = useState(false);
  const { busy, error, run } = useAction();
  const ssh = integ.connection === 'ssh' || integ.permissions?.ssh;

  useEffect(() => { get<Credential[]>('/api/credentials').then(setCreds).catch(() => {}); }, []);
  useEffect(() => {
    if (!def) return;
    setValues(def.settings ?? {});
    setConn({ port: 22, sudo: false, ...(def.connection ?? {}) });
  }, [def]);

  const save = (e: FormEvent) => {
    e.preventDefault();
    setSaved(false);
    run(async () => {
      await put(`/api/integrations/${integ.id}/defaults`, { settings: cleanSettings(values), connection: conn.credentialId ? conn : {} });
      setSaved(true);
    });
  };
  const sshCreds = creds.filter((c) => c.type !== 'token');
  const shared = (integ.settings ?? []).filter((s) => s.scope !== 'device');
  const perDevice = (integ.settings ?? []).filter((s) => s.scope === 'device');
  const credSettings = shared.filter((s) => s.type === 'credential');
  // The guide starts open until something is set up.
  const fresh = !!def && Object.keys(def.settings ?? {}).length === 0 && !def.connection?.credentialId;

  return (
    <Modal wide title={t('integSetup.title', { name: tx(integ.name, lang) })} onClose={onClose} footer={
      <>
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.close')}</button>
        <button className="btn" type="submit" form="integ-setup-form" disabled={busy || !def}>{t('common.save')}</button>
      </>
    }>
      {!def ? <Spinner /> : (
        <form id="integ-setup-form" className="stack" onSubmit={save}>
          <p className="small muted">{t('integSetup.intro')}</p>
          {integ.howto && (
            <details open={fresh} className="howto-box">
              <summary>{t('integSetup.guide')}</summary>
              <pre className="howto">{tx(integ.howto, lang)}</pre>
            </details>
          )}

          {ssh && (
            <fieldset className="stack" style={{ gap: 10 }}>
              <legend><strong>{t('integSetup.connection')}</strong></legend>
              <p className="small muted" style={{ margin: 0 }}>{t('integSetup.connectionHint')}</p>
              <div className="row" style={{ alignItems: 'flex-end' }}>
                <label className="field" style={{ flex: '2 1 220px' }}>
                  {t('conn.credential')}
                  <select id="setup-cred" value={conn.credentialId ?? ''} onChange={(e) => setConn({ ...conn, credentialId: e.target.value ? Number(e.target.value) : undefined })}>
                    <option value="">{t('integSetup.noConnection')}</option>
                    {sshCreds.map((c) => <option key={c.id} value={c.id}>{c.name} ({t('credType.' + c.type)})</option>)}
                  </select>
                </label>
                {can('credentials.manage') && (
                  <button className="btn ghost" type="button" onClick={() => setCreating({ types: ['ssh_key', 'password'], apply: (c) => setConn((cur) => ({ ...cur, credentialId: c.id, user: cur.user || c.username || '' })) })}>
                    <IconPlus />{t('conn.newCredential')}
                  </button>
                )}
                <label className="field" style={{ flex: '1 1 120px' }}>
                  {t('conn.user')}
                  <input id="setup-user" type="text" value={conn.user ?? ''} placeholder={sshCreds.find((c) => c.id === conn.credentialId)?.username || 'pi'} onChange={(e) => setConn({ ...conn, user: e.target.value })} />
                </label>
                <label className="field" style={{ flex: '0 1 90px' }}>
                  {t('deviceForm.port')}
                  <input id="setup-port" type="number" min={1} max={65535} value={conn.port ?? 22} onChange={(e) => setConn({ ...conn, port: Number(e.target.value) })} />
                </label>
              </div>
              <label className="check"><input id="setup-sudo" type="checkbox" checked={!!conn.sudo} onChange={(e) => setConn({ ...conn, sudo: e.target.checked })} />{t('conn.sudo')}</label>
            </fieldset>
          )}

          {shared.length > 0 && (
            <fieldset className="stack" style={{ gap: 10 }}>
              <legend><strong>{t('integSetup.settings')}</strong></legend>
              <p className="small muted" style={{ margin: 0 }}>{t('integSetup.settingsHint')}</p>
              {shared.map((s) => (
                <SettingField key={s.key} idPrefix="setup-" s={s} value={values[s.key]} creds={creds} inherited={s.default}
                  onChange={(v) => setValues({ ...values, [s.key]: v })} />
              ))}
              {credSettings.length > 0 && can('credentials.manage') && (
                <div className="row">
                  {credSettings.map((s) => (
                    <button key={s.key} className="btn ghost sm" type="button" onClick={() => setCreating({
                      types: (s.credentialTypes?.length ? s.credentialTypes : ['token', 'password']) as Credential['type'][],
                      apply: (c) => setValues((cur) => ({ ...cur, [s.key]: c.id })),
                    })}><IconPlus />{t('integSetup.newFor', { name: tx(s.label, lang) })}</button>
                  ))}
                </div>
              )}
            </fieldset>
          )}

          {perDevice.length > 0 && (
            <p className="small muted" style={{ margin: 0 }}>{t('integSetup.perDevice', { list: perDevice.map((s) => tx(s.label, lang)).join(', ') })}</p>
          )}
          {!ssh && shared.length === 0 && <p className="small muted">{t('integSetup.nothing')}</p>}

          <div className="stack" style={{ gap: 6 }}>
            <strong>{t('integSetup.devices', { n: def.devices.length })}</strong>
            {def.devices.length === 0 ? <p className="small muted" style={{ margin: 0 }}>{t('integSetup.noDevices')}</p> : (
              <ul className="plain small">
                {def.devices.map((d) => (
                  <li key={d.id}>
                    <Link to={`/devices/${d.id}`} onClick={onClose}>{d.name}</Link>
                    {(d.own.length > 0 || d.ownConnection) && (
                      <span className="muted"> · {t('integSetup.ownValues', {
                        list: [...(d.ownConnection ? [t('integSetup.connection')] : []), ...d.own.map((k) => tx(integ.settings?.find((s) => s.key === k)?.label, lang) || k)].join(', '),
                      })}</span>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>

          <ErrorAlert error={error} />
          {saved && <div className="alert ok">{t('integSetup.saved')}</div>}
        </form>
      )}
      {creating && <CredentialModal cred={null} types={creating.types} onClose={() => setCreating(null)} onSaved={(c) => {
        setCreds((cur) => [...cur.filter((x) => x.id !== c.id), c]);
        creating.apply(c);
        setCreating(null);
      }} />}
    </Modal>
  );
}
