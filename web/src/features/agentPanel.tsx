import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { ApiError, del, get, post, put, type AgentStatus, type Device } from '../api';
import { formatAgo, useLang, useT } from '../i18n';
import { useSession } from '../session';
import { ConfirmButton, ErrorAlert, Modal, useAction } from '../components/ui';

function Copyable({ text }: { text: string }) {
  const t = useT();
  const [done, setDone] = useState(false);
  return (
    <div className="stack" style={{ gap: 6 }}>
      <pre className="log" style={{ whiteSpace: 'pre-wrap' }}>{text}</pre>
      <div><button type="button" className="btn ghost sm" onClick={() => navigator.clipboard?.writeText(text).then(() => { setDone(true); setTimeout(() => setDone(false), 1500); }, () => {})}>{done ? t('common.copied') : t('common.copy')}</button></div>
    </div>
  );
}

/** Installs the Linux agent over SSH from the GUI, no terminal needed. */
function InstallOverSSH({ device, onClose, onDone }: { device: Device; onClose: () => void; onDone: () => void }) {
  const t = useT();
  const hasConn = !!(device.config as { connection?: { credentialId?: number } }).connection?.credentialId;
  const [mode, setMode] = useState<'password' | 'saved'>(hasConn ? 'saved' : 'password');
  const [user, setUser] = useState('root');
  const [password, setPassword] = useState('');
  const [port, setPort] = useState(22);
  const [server, setServer] = useState(location.origin);
  const [insecure, setInsecure] = useState(false);
  const [output, setOutput] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const { busy, error, run } = useAction();
  const failedOutput = error instanceof ApiError ? (error.detail as { output?: string } | undefined)?.output : undefined;

  const start = (e: FormEvent) => {
    e.preventDefault();
    setOutput(null);
    run(async () => {
      const body = mode === 'password' ? { server, insecure, user, password, port } : { server, insecure };
      const r = await post<{ output: string }>(`/api/devices/${device.id}/agent/install-ssh`, body);
      setOutput(r.output);
      setOk(true);
      setPassword('');
      onDone();
    });
  };
  return (
    <Modal wide title={t('agentSsh.title', { name: device.name })} onClose={onClose} footer={
      ok ? <button className="btn" type="button" onClick={onClose}>{t('common.done')}</button> : (
        <>
          <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
          <button id="agent-ssh-start" className="btn" type="submit" form="agent-ssh-form" disabled={busy}>{busy ? t('agentSsh.running') : t('agentSsh.start')}</button>
        </>
      )
    }>
      {ok ? (
        <div className="alert ok">{t('agentSsh.ok')}</div>
      ) : (
        <form id="agent-ssh-form" className="stack" onSubmit={start}>
          <p className="small muted" style={{ margin: 0 }}>{t('agentSsh.intro')}</p>
          <div className="stack" style={{ gap: 8 }}>
            <label className="check"><input type="radio" name="agent-ssh-mode" checked={mode === 'password'} onChange={() => setMode('password')} />{t('agentSsh.modePassword')}</label>
            <label className="check"><input type="radio" name="agent-ssh-mode" checked={mode === 'saved'} onChange={() => setMode('saved')} />{t('agentSsh.modeSaved')}</label>
          </div>
          {mode === 'password' && (
            <div className="row" style={{ alignItems: 'flex-end' }}>
              <label className="field" style={{ flex: '1 1 140px' }}>{t('conn.user')}
                <input id="agent-ssh-user" type="text" value={user} onChange={(e) => setUser(e.target.value)} required />
              </label>
              <label className="field" style={{ flex: '2 1 200px' }}>{t('cred.password')}
                <input id="agent-ssh-password" type="password" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} required />
              </label>
              <label className="field" style={{ flex: '0 1 90px' }}>{t('deviceForm.port')}
                <input id="agent-ssh-port" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(Number(e.target.value))} />
              </label>
            </div>
          )}
          {mode === 'password' && <p className="small muted" style={{ margin: 0 }}>{t('agentSsh.passwordHint')}</p>}
          {mode === 'saved' && <p className="small muted" style={{ margin: 0 }}>{t('agentSsh.savedHint')}</p>}
          <label className="field">{t('agentSsh.server')}
            <input id="agent-ssh-server" type="url" className="mono" value={server} onChange={(e) => setServer(e.target.value)} required />
            <span className="hint">{t('agentSsh.serverHint')}</span>
          </label>
          {server.startsWith('https:') && <label className="check small"><input type="checkbox" checked={insecure} onChange={(e) => setInsecure(e.target.checked)} />{t('agent.insecure')}</label>}
          <ErrorAlert error={error} />
          {failedOutput && <pre className="log" style={{ maxHeight: '30vh' }}>{failedOutput}</pre>}
        </form>
      )}
      {output && <details><summary className="small" style={{ cursor: 'pointer' }}>{t('agentSsh.output')}</summary><pre className="log" style={{ marginTop: 8 }}>{output}</pre></details>}
    </Modal>
  );
}

/** Agent status, install commands and revoke, shown in the Connection card. */
export function AgentPanel({ device }: { device: Device }) {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const [st, setSt] = useState<AgentStatus | null>(null);
  const [token, setToken] = useState('');
  const [insecure, setInsecure] = useState(false);
  const [viaSSH, setViaSSH] = useState(false);
  const { busy, error, run } = useAction();
  const load = useCallback(() => { get<AgentStatus>(`/api/devices/${device.id}/agent`).then(setSt).catch(() => {}); }, [device.id]);
  useEffect(() => {
    load();
    const i = setInterval(load, 5000);
    return () => clearInterval(i);
  }, [load]);
  useEffect(() => { if (st?.enrolled && token) setToken(''); }, [st?.enrolled]); // eslint-disable-line react-hooks/exhaustive-deps
  const manage = can('devices.manage');
  const server = location.origin;
  const k = insecure ? ' --insecure' : '';
  const linux = `curl -fsSL${insecure ? 'k' : ''} ${server}/agent/install.sh | sudo sh -s -- ${server} ${token}${k}`;
  const windows = `& ([scriptblock]::Create((irm ${server}/agent/install.ps1))) ${server} ${token}`;
  const a = st?.agent;

  return (
    <div className="stack">
      <p className="small muted">{t('agent.intro')}</p>
      {st && (
        <div className="row">
          <span className={'pill dot ' + (st.online ? 'ok' : st.enrolled ? 'off' : 'off')}>{st.online ? t('agent.connected') : st.enrolled ? t('agent.disconnected') : t('agent.notInstalled')}</span>
          {a && st.enrolled && <span className="small muted">{a.hostname} · {a.os}/{a.arch} · {a.version}{a.lastSeen ? ' · ' + formatAgo(lang, a.lastSeen) : ''}</span>}
        </div>
      )}
      {manage && (
        <div className="row">
          <button id="agent-install-ssh" className="btn" type="button" disabled={busy || !device.host} onClick={() => setViaSSH(true)} title={device.host ? undefined : t('conn.needHost')}>
            {st?.enrolled ? t('agentSsh.reinstallButton') : t('agentSsh.button')}
          </button>
          <button className="btn ghost" type="button" disabled={busy} onClick={() => run(async () => { const r = await post<{ token: string }>(`/api/devices/${device.id}/agent/enroll`); setToken(r.token); })}>
            {t('agentSsh.manual')}
          </button>
          {device.presence.reach.method !== 'agent' && st?.enrolled && (
            <button className="btn subtle" type="button" disabled={busy} onClick={() => run(async () => {
              await put(`/api/devices/${device.id}`, { ...device, presence: { ...device.presence, reach: { method: 'agent' } } });
            })}>{t('agent.usePresence')}</button>
          )}
          {st?.enrolled && <ConfirmButton label={t('agent.revoke')} onConfirm={() => run(async () => { await del(`/api/devices/${device.id}/agent`); load(); })} />}
        </div>
      )}
      {token && (
        <div className="stack">
          <p className="small">{t('agent.commandIntro')}</p>
          <label className="check small"><input type="checkbox" checked={insecure} onChange={(e) => setInsecure(e.target.checked)} />{t('agent.insecure')}</label>
          <strong className="small">Linux / Raspberry Pi / Unraid / Proxmox</strong>
          <Copyable text={linux} />
          <strong className="small">Windows ({t('agent.adminPs')})</strong>
          <Copyable text={windows} />
          <p className="small muted">{t('agent.tokenHint')}</p>
        </div>
      )}
      <ErrorAlert error={error} />
      {viaSSH && <InstallOverSSH device={device} onClose={() => setViaSSH(false)} onDone={load} />}
    </div>
  );
}
