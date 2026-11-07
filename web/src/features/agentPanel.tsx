import { useCallback, useEffect, useState } from 'react';
import { del, get, post, put, type AgentStatus, type Device } from '../api';
import { formatAgo, useLang, useT } from '../i18n';
import { useSession } from '../session';
import { ConfirmButton, ErrorAlert, copyText, useAction } from '../components/ui';

function Copyable({ text }: { text: string }) {
  const t = useT();
  const [done, setDone] = useState(false);
  return (
    <div className="stack" style={{ gap: 6 }}>
      <pre className="log" style={{ whiteSpace: 'pre-wrap' }}>{text}</pre>
      <div><button type="button" className="btn ghost sm" onClick={() => copyText(text).then((ok) => { if (ok) { setDone(true); setTimeout(() => setDone(false), 1500); } })}>{done ? t('common.copied') : t('common.copy')}</button></div>
    </div>
  );
}

/** Agent status, install commands and revoke, shown in the Connection card. */
export function AgentPanel({ device }: { device: Device }) {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const [st, setSt] = useState<AgentStatus | null>(null);
  const [token, setToken] = useState('');
  const [insecure, setInsecure] = useState(location.protocol === 'https:' ? false : false);
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
          <button className="btn" type="button" disabled={busy} onClick={() => run(async () => { const r = await post<{ token: string }>(`/api/devices/${device.id}/agent/enroll`); setToken(r.token); })}>
            {st?.enrolled ? t('agent.reinstall') : t('agent.install')}
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
    </div>
  );
}
