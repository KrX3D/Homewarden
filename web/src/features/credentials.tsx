// Credentials vault (settings tab) and the per-device SSH connection card.
import { useEffect, useState, type FormEvent } from 'react';
import { del, get, post, put, ApiError, type ConnectionSettings, type Credential, type CredentialType, type Device, type Facts } from '../api';
import { errorText, formatTime, useLang, useT } from '../i18n';
import { useSession } from '../session';
import { ConfirmButton, ErrorAlert, Modal, useAction } from '../components/ui';
import { IconEdit, IconKey, IconPlus } from '../components/icons';
import { deviceSections, settingsTabs } from '../extensions';

function CopyButton({ text }: { text: string }) {
  const t = useT();
  const [done, setDone] = useState(false);
  return (
    <button type="button" className="btn ghost sm" onClick={() => {
      navigator.clipboard?.writeText(text).then(() => { setDone(true); setTimeout(() => setDone(false), 1500); }, () => {});
    }}>{done ? t('common.copied') : t('common.copy')}</button>
  );
}

// ---------------------------------------------------------------- vault tab

function CredentialsTab() {
  const t = useT();
  const lang = useLang();
  const [list, setList] = useState<Credential[]>([]);
  const [editing, setEditing] = useState<Credential | 'new' | null>(null);
  const { error, run } = useAction();
  const load = () => run(async () => setList(await get<Credential[]>('/api/credentials')));
  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <section className="stack">
      <div className="page-head">
        <p className="muted grow">{t('cred.intro')}</p>
        <button className="btn" type="button" onClick={() => setEditing('new')}><IconPlus />{t('cred.add')}</button>
      </div>
      <ErrorAlert error={error} />
      {list.length === 0 ? (
        <div className="card empty"><IconKey /><h2>{t('cred.emptyTitle')}</h2><p>{t('cred.emptyText')}</p></div>
      ) : (
        <div className="table-wrap">
          <table className="tbl">
            <thead><tr><th>{t('cred.name')}</th><th>{t('cred.type')}</th><th>{t('cred.username')}</th><th>{t('cred.usedBy')}</th><th>{t('cred.changed')}</th><th></th></tr></thead>
            <tbody>
              {list.map((c) => (
                <tr key={c.id}>
                  <td><strong>{c.name}</strong></td>
                  <td><span className="chip">{t('credType.' + c.type)}</span></td>
                  <td className="mono small">{c.username || '–'}</td>
                  <td>{t('cred.devices', { n: c.usedBy })}</td>
                  <td className="small muted">{formatTime(lang, c.updatedAt)}</td>
                  <td style={{ textAlign: 'right' }}><button className="btn subtle sm" type="button" aria-label={t('common.edit')} onClick={() => setEditing(c)}><IconEdit /></button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing && <CredentialModal cred={editing === 'new' ? null : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
    </section>
  );
}

function CredentialModal({ cred, onClose, onSaved }: { cred: Credential | null; onClose: () => void; onSaved: (c: Credential) => void }) {
  const t = useT();
  const [type, setType] = useState<CredentialType>(cred?.type ?? 'ssh_key');
  const [name, setName] = useState(cred?.name ?? '');
  const [username, setUsername] = useState(cred?.username ?? '');
  const [secret, setSecret] = useState('');
  const [keyMode, setKeyMode] = useState<'generate' | 'paste'>('generate');
  const [created, setCreated] = useState<Credential | null>(null);
  const { busy, error, run } = useAction();

  const save = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      const body = { name, type, username, secret, generate: type === 'ssh_key' && keyMode === 'generate' && !cred };
      const out = cred ? await put<Credential>(`/api/credentials/${cred.id}`, body) : await post<Credential>('/api/credentials', body);
      if (!cred && out.type === 'ssh_key') setCreated(out);
      else onSaved(out);
    });
  };

  if (created) {
    return (
      <Modal title={t('cred.keyCreated')} onClose={() => onSaved(created)} footer={<button className="btn" type="button" onClick={() => onSaved(created)}>{t('common.done')}</button>}>
        <p className="small">{t('cred.keyCreatedText')}</p>
        <textarea readOnly rows={3} value={created.public} onFocus={(e) => e.currentTarget.select()} />
        <div><CopyButton text={created.public} /></div>
      </Modal>
    );
  }

  return (
    <Modal title={cred ? t('cred.edit') : t('cred.add')} onClose={onClose} footer={
      <>
        {cred && <ConfirmButton label={t('common.delete')} onConfirm={() => run(async () => { await del(`/api/credentials/${cred.id}`); onSaved(cred); })} />}
        <span style={{ flex: 1 }} />
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button className="btn" type="submit" form="cred-form" disabled={busy}>{t('common.save')}</button>
      </>
    }>
      <form id="cred-form" className="stack" onSubmit={save}>
        {!cred && (
          <label className="field">
            {t('cred.type')}
            <select id="cred-type" value={type} onChange={(e) => setType(e.target.value as CredentialType)}>
              <option value="ssh_key">{t('credType.ssh_key')}</option>
              <option value="password">{t('credType.password')}</option>
              <option value="token">{t('credType.token')}</option>
            </select>
            <span className="hint">{t('credTypeHint.' + type)}</span>
          </label>
        )}
        <label className="field">
          {t('cred.name')}
          <input id="cred-name" type="text" value={name} onChange={(e) => setName(e.target.value)} required maxLength={128} placeholder={type === 'ssh_key' ? 'Homewarden key' : 'OctoPrint API key'} />
        </label>
        <label className="field">
          {t('cred.username')}
          <input id="cred-username" type="text" value={username} onChange={(e) => setUsername(e.target.value)} placeholder="pi" />
          <span className="hint">{t('cred.usernameHint')}</span>
        </label>
        {type === 'ssh_key' && !cred && (
          <div className="stack" style={{ gap: 8 }}>
            <label className="check"><input type="radio" name="keymode" checked={keyMode === 'generate'} onChange={() => setKeyMode('generate')} />{t('cred.generate')}</label>
            <label className="check"><input type="radio" name="keymode" checked={keyMode === 'paste'} onChange={() => setKeyMode('paste')} />{t('cred.paste')}</label>
          </div>
        )}
        {type === 'ssh_key' && cred && (
          <div className="stack" style={{ gap: 6 }}>
            <span className="small" style={{ fontWeight: 500 }}>{t('cred.publicKey')}</span>
            <textarea readOnly rows={3} value={cred.public} onFocus={(e) => e.currentTarget.select()} />
            <div><CopyButton text={cred.public} /></div>
          </div>
        )}
        {(type !== 'ssh_key' || keyMode === 'paste' || cred) && (
          <label className="field">
            {type === 'ssh_key' ? t('cred.privateKey') : type === 'token' ? t('cred.token') : t('cred.password')}
            {type === 'ssh_key'
              ? <textarea id="cred-secret" rows={5} value={secret} onChange={(e) => setSecret(e.target.value)} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" required={!cred} />
              : <input id="cred-secret" type="password" autoComplete="new-password" value={secret} onChange={(e) => setSecret(e.target.value)} required={!cred} />}
            {cred && <span className="hint">{t('cred.keepSecret')}</span>}
          </label>
        )}
        <ErrorAlert error={error} />
      </form>
    </Modal>
  );
}

settingsTabs.push({ path: 'credentials', labelKey: 'settings.credentials', perm: 'credentials.manage', element: <CredentialsTab /> });

// ---------------------------------------------------------------- device connection card

function ConnectionCard({ device }: { device: Device }) {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const manage = can('devices.manage');
  const initial: ConnectionSettings = { method: 'ssh', port: 22, sudo: false, ...((device.config as { connection?: ConnectionSettings }).connection ?? {}) };
  const [set, setSet] = useState<ConnectionSettings>(initial);
  const [creds, setCreds] = useState<Credential[]>([]);
  const [facts, setFacts] = useState<Facts | null>(null);
  const [hostKey, setHostKey] = useState<{ fingerprint: string; keyType: string; firstSeen: number } | null>(null);
  const [installing, setInstalling] = useState(false);
  const [saved, setSaved] = useState(false);
  const save = useAction();
  const test = useAction();

  // Re-sync when the device was changed elsewhere (e.g. by Install key).
  const cfgKey = JSON.stringify((device.config as { connection?: ConnectionSettings }).connection ?? {});
  useEffect(() => { setSet(initial); }, [cfgKey]); // eslint-disable-line react-hooks/exhaustive-deps

  const loadKey = () => get<typeof hostKey>(`/api/devices/${device.id}/hostkey`).then(setHostKey).catch(() => {});
  useEffect(() => {
    if (manage) get<Credential[]>('/api/credentials').then(setCreds).catch(() => {});
    loadKey();
  }, [device.id]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!device.host) return null;
  const configured = !!initial.credentialId;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setSaved(false);
    save.run(async () => {
      await put(`/api/devices/${device.id}/connection`, { ...set, method: 'ssh' });
      setSaved(true);
    });
  };
  const runTest = () => test.run(async () => {
    setFacts(null);
    setFacts(await post<Facts>(`/api/devices/${device.id}/connection/test`));
    loadKey();
  });
  const keyChanged = test.error instanceof ApiError && test.error.code === 'host_key_changed' ? (test.error.detail as { expected: string; got: string }) : null;

  return (
    <section className="card">
      <div className="card-head">
        <h2 className="grow">{t('conn.title')}</h2>
        <span className={'pill ' + (configured ? 'accent' : 'off')}>{configured ? 'SSH' : t('conn.notSet')}</span>
      </div>
      <p className="small muted">{t('conn.intro')}</p>
      {manage ? (
        <form className="stack" onSubmit={submit}>
          <div className="row" style={{ alignItems: 'flex-end' }}>
            <label className="field" style={{ flex: '2 1 200px' }}>
              {t('conn.credential')}
              <select id="conn-cred" value={set.credentialId ?? ''} onChange={(e) => setSet({ ...set, credentialId: e.target.value ? Number(e.target.value) : undefined })}>
                <option value="">{t('conn.pickCredential')}</option>
                {creds.filter((c) => c.type !== 'token').map((c) => <option key={c.id} value={c.id}>{c.name} ({t('credType.' + c.type)})</option>)}
              </select>
            </label>
            <label className="field" style={{ flex: '1 1 120px' }}>
              {t('conn.user')}
              <input id="conn-user" type="text" value={set.user ?? ''} placeholder={creds.find((c) => c.id === set.credentialId)?.username || 'pi'} onChange={(e) => setSet({ ...set, user: e.target.value })} />
            </label>
            <label className="field" style={{ flex: '0 1 90px' }}>
              {t('deviceForm.port')}
              <input id="conn-port" type="number" min={1} max={65535} value={set.port ?? 22} onChange={(e) => setSet({ ...set, port: Number(e.target.value) })} />
            </label>
          </div>
          <label className="check"><input id="conn-sudo" type="checkbox" checked={set.sudo} onChange={(e) => setSet({ ...set, sudo: e.target.checked })} />{t('conn.sudo')}</label>
          <p className="small muted">{t('conn.sudoHint')}</p>
          <ErrorAlert error={save.error} />
          {saved && <div className="alert ok">{t('conn.saved')}</div>}
          <div className="row">
            <button className="btn" type="submit" disabled={save.busy}>{t('common.save')}</button>
            <button className="btn ghost" type="button" onClick={runTest} disabled={test.busy || !configured}>{test.busy ? t('conn.testing') : t('conn.test')}</button>
            <button className="btn subtle" type="button" onClick={() => setInstalling(true)}><IconKey />{t('conn.installKey')}</button>
          </div>
        </form>
      ) : (
        configured && <div><button className="btn ghost" type="button" onClick={runTest} disabled={test.busy}>{t('conn.test')}</button></div>
      )}

      {keyChanged ? (
        <div className="alert error stack" style={{ gap: 8 }}>
          <strong>{t('conn.keyChangedTitle')}</strong>
          <span>{t('conn.keyChangedText', { expected: keyChanged.expected, got: keyChanged.got })}</span>
          {manage && <div><ConfirmButton className="btn ghost sm" label={t('conn.trustNewKey')} onConfirm={async () => { await del(`/api/devices/${device.id}/hostkey`); runTest(); }} /></div>}
        </div>
      ) : (
        test.error ? <div className="alert error">{errorText(t, test.error)}</div> : null
      )}

      {facts && (
        <div className="alert ok stack" style={{ gap: 6 }}>
          <strong>{t('conn.ok', { user: facts.user, host: facts.hostname })}</strong>
          <dl className="kv">
            <dt>{t('conn.os')}</dt><dd>{facts.os || '–'}</dd>
            <dt>{t('conn.kernel')}</dt><dd className="mono">{facts.kernel} ({facts.arch})</dd>
            <dt>sudo</dt><dd>{facts.sudo ? t('conn.sudoYes') : t('conn.sudoNo')}</dd>
          </dl>
          {facts.hostKeyFirst && <span className="small">{t('conn.firstKey')}</span>}
        </div>
      )}
      {hostKey && (
        <p className="small muted">{t('conn.hostKey')} <code>{hostKey.keyType} {hostKey.fingerprint}</code> · {t('conn.pinned', { when: formatTime(lang, hostKey.firstSeen) })}</p>
      )}

      {installing && <InstallKeyModal device={device} creds={creds} onClose={() => setInstalling(false)} onDone={(c, user, port) => { setInstalling(false); setSet({ ...set, method: 'ssh', credentialId: c.id, user, port }); setCreds((cur) => cur.some((x) => x.id === c.id) ? cur : [...cur, c]); runTest(); }} />}
    </section>
  );
}

function InstallKeyModal({ device, creds, onClose, onDone }: { device: Device; creds: Credential[]; onClose: () => void; onDone: (c: Credential, user: string, port: number) => void }) {
  const t = useT();
  const { can } = useSession();
  const keys = creds.filter((c) => c.type === 'ssh_key');
  const [credId, setCredId] = useState<number | 'new'>(keys[0]?.id ?? 'new');
  const [user, setUser] = useState('pi');
  const [password, setPassword] = useState('');
  const [port, setPort] = useState(22);
  const { busy, error, run } = useAction();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      let cred = keys.find((k) => k.id === credId);
      if (!cred) cred = await post<Credential>('/api/credentials', { name: `Homewarden key (${device.name})`, type: 'ssh_key', username: user, generate: true });
      await post(`/api/devices/${device.id}/connection/install-key`, { user, password, port, credentialId: cred.id });
      setPassword('');
      onDone(cred, user, port);
    });
  };
  return (
    <Modal title={t('install.title')} onClose={onClose} footer={
      <>
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button className="btn" type="submit" form="install-form" disabled={busy}>{busy ? t('install.working') : t('install.submit')}</button>
      </>
    }>
      <form id="install-form" className="stack" onSubmit={submit}>
        <p className="small">{t('install.intro', { host: device.host })}</p>
        <ol className="small muted" style={{ margin: 0, paddingLeft: 18, display: 'grid', gap: 4 }}>
          <li>{t('install.step1')}</li>
          <li>{t('install.step2')}</li>
          <li>{t('install.step3')}</li>
        </ol>
        <label className="field">
          {t('install.key')}
          <select id="install-key" value={credId} onChange={(e) => setCredId(e.target.value === 'new' ? 'new' : Number(e.target.value))}>
            {keys.map((k) => <option key={k.id} value={k.id}>{k.name}</option>)}
            {can('credentials.manage') && <option value="new">{t('install.newKey')}</option>}
          </select>
        </label>
        <div className="row">
          <label className="field" style={{ flex: '2 1 160px' }}>
            {t('conn.user')}
            <input id="install-user" type="text" value={user} onChange={(e) => setUser(e.target.value)} required />
          </label>
          <label className="field" style={{ flex: '0 1 90px' }}>
            {t('deviceForm.port')}
            <input id="install-port" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(Number(e.target.value))} />
          </label>
        </div>
        <label className="field">
          {t('install.password')}
          <input id="install-password" type="password" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} required />
          <span className="hint">{t('install.passwordHint')}</span>
        </label>
        <ErrorAlert error={error} />
      </form>
    </Modal>
  );
}

deviceSections.push(ConnectionCard);
