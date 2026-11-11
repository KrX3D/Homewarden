// Update policy per device, API tokens and MQTT settings.
import { useEffect, useState, type FormEvent } from 'react';
import { del, get, post, put, type APIToken, type Credential, type Device, type MQTTSettings, type Policy } from '../api';
import { formatAgo, formatTime, useLang, useT } from '../i18n';
import { useSession } from '../session';
import { ConfirmButton, ErrorAlert, copyText, useAction } from '../components/ui';
import { IconPlus } from '../components/icons';
import { deviceSections, settingsTabs } from '../extensions';

const PRESETS = ['0 3 * * *', '0 3 * * 0', '0 4 1 * *'];

function AutomationCard({ device }: { device: Device }) {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const saved: Policy = { mode: 'manual', schedule: '0 3 * * 0', scope: 'security', ...((device.config as { policy?: Policy }).policy ?? {}) };
  const [p, setP] = useState<Policy>(saved);
  const [pending, setPending] = useState<{ action: string; since: number }[]>([]);
  const [ok, setOk] = useState(false);
  const [offMin, setOffMin] = useState<number>(((device.config as { notify?: { offlineMinutes?: number } }).notify?.offlineMinutes) ?? 0);
  const { busy, error, run } = useAction();
  useEffect(() => setP(saved), [JSON.stringify(saved)]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { get<typeof pending>(`/api/devices/${device.id}/pending`).then(setPending).catch(() => {}); }, [device.id, device.state]);
  if (device.kind === 'generic') return null;
  const manage = can('devices.manage');

  return (
    <section className="card">
      <h2>{t('auto.title')}</h2>
      <form className="stack" onSubmit={(e: FormEvent) => { e.preventDefault(); setOk(false); run(async () => { await put(`/api/devices/${device.id}/policy`, p); await put(`/api/devices/${device.id}/notify`, { offlineMinutes: offMin }); setOk(true); }); }}>
        <label className="field">{t('auto.mode')}
          <select id="pol-mode" value={p.mode} disabled={!manage} onChange={(e) => setP({ ...p, mode: e.target.value as Policy['mode'] })}>
            <option value="manual">{t('auto.manual')}</option>
            <option value="auto">{t('auto.auto')}</option>
            <option value="scheduled">{t('auto.scheduled')}</option>
          </select>
          <span className="hint">{t('auto.hint.' + p.mode)}</span>
        </label>
        {p.mode === 'scheduled' && (
          <label className="field">{t('auto.schedule')}
            <input id="pol-schedule" type="text" className="mono" value={p.schedule} disabled={!manage} onChange={(e) => setP({ ...p, schedule: e.target.value })} />
            <span className="row" style={{ gap: 6 }}>
              {PRESETS.map((x) => <button key={x} type="button" className="btn subtle sm" disabled={!manage} onClick={() => setP({ ...p, schedule: x })}>{t('auto.preset.' + x)}</button>)}
            </span>
            <span className="hint">{t('auto.cronHint')}</span>
          </label>
        )}
        {p.mode !== 'manual' && (
          <label className="field">{t('auto.scope')}
            <select id="pol-scope" value={p.scope} disabled={!manage} onChange={(e) => setP({ ...p, scope: e.target.value as Policy['scope'] })}>
              <option value="security">{t('auto.scopeSecurity')}</option>
              <option value="all">{t('auto.scopeAll')}</option>
            </select>
            <span className="hint">{t('auto.scopeHint')}</span>
          </label>
        )}
        <label className="field">{t('auto.offline')}
          <input id="pol-offline" type="number" min={0} max={10080} value={offMin} disabled={!manage} onChange={(e) => setOffMin(Number(e.target.value))} style={{ maxWidth: 140 }} />
          <span className="hint">{t('auto.offlineHint')}</span>
        </label>
        {ok && <div className="alert ok">{t('conn.saved')}</div>}
        <ErrorAlert error={error} />
        {manage && <div><button className="btn" type="submit" disabled={busy}>{t('common.save')}</button></div>}
      </form>
      {pending.length > 0 && (
        <div className="alert info">{t('auto.pending', { list: pending.map((x) => t('auto.action.' + x.action)).join(', '), since: formatAgo(lang, pending[0].since) })}</div>
      )}
      <details>
        <summary className="small" style={{ cursor: 'pointer' }}>{t('auto.external')}</summary>
        <div className="stack small" style={{ marginTop: 8 }}>
          <span>{t('auto.apiHint')}</span>
          <pre className="log">{`curl -X POST -H "Authorization: Bearer hw_..." \\\n  -H "Content-Type: application/json" \\\n  -d '{"action":"update","scope":"security"}' \\\n  ${location.origin}/api/devices/${device.id}/trigger`}</pre>
          <span>{t('auto.mqttHint', { topic: `homewarden/device/${device.name.toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_|_$/g, '')}/cmd` })}</span>
        </div>
      </details>
    </section>
  );
}

function TokensTab() {
  const t = useT();
  const lang = useLang();
  const [list, setList] = useState<APIToken[]>([]);
  const [name, setName] = useState('');
  const [readOnly, setReadOnly] = useState(false);
  const [created, setCreated] = useState('');
  const [copied, setCopied] = useState(false);
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const { can } = useSession();
  const admin = can('settings.manage');
  const { busy, error, run } = useAction();
  const load = () => get<APIToken[]>('/api/me/tokens').then(setList).catch(() => {});
  useEffect(() => {
    load();
    if (admin) get<{ enabled: boolean }>('/api/settings/api').then((r) => setEnabled(r.enabled)).catch(() => {});
  }, [admin]);
  const toggle = (on: boolean) => run(async () => { const r = await put<{ enabled: boolean }>('/api/settings/api', { enabled: on }); setEnabled(r.enabled); });
  return (
    <section className="stack">
      {admin && enabled !== null && (
        <div className={'card stack' + (enabled ? '' : ' api-off')} style={{ gap: 6 }}>
          <label className="check"><input id="api-enabled" type="checkbox" checked={enabled} disabled={busy} onChange={(e) => toggle(e.target.checked)} /><strong>{t('api.enabled')}</strong></label>
          <span className="small muted">{enabled ? t('api.enabledHint') : t('api.disabledHint')}</span>
        </div>
      )}
      <p className="muted">{t('tokens.intro')}</p>
      <form className="row" onSubmit={(e) => { e.preventDefault(); run(async () => { const r = await post<{ token: string }>('/api/me/tokens', { name, readOnly }); setCreated(r.token); setName(''); setReadOnly(false); load(); }); }}>
        <input id="token-name" type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="Home Assistant" required style={{ maxWidth: 280 }} />
        <label className="check small"><input id="token-readonly" type="checkbox" checked={readOnly} onChange={(e) => setReadOnly(e.target.checked)} />{t('tokens.readOnly')}</label>
        <button className="btn" type="submit" disabled={busy}><IconPlus />{t('tokens.create')}</button>
      </form>
      {created && (
        <div className="alert ok stack" style={{ gap: 6 }}>
          <strong>{t('tokens.copyNow')}</strong>
          <div className="row" style={{ flexWrap: 'nowrap' }}>
            <code className="grow" style={{ overflowWrap: 'anywhere' }}>{created}</code>
            <button id="token-copy" className="btn ghost sm" type="button" onClick={() => copyText(created).then((ok) => { if (ok) { setCopied(true); setTimeout(() => setCopied(false), 1500); } })}>{copied ? t('common.copied') : t('common.copy')}</button>
          </div>
        </div>
      )}
      <ErrorAlert error={error} />
      {list.length > 0 && (
        <div className="table-wrap"><table className="tbl">
          <thead><tr><th>{t('cred.name')}</th><th>{t('tokens.access')}</th><th>{t('tokens.created')}</th><th>{t('tokens.lastUsed')}</th><th></th></tr></thead>
          <tbody>{list.map((x) => (
            <tr key={x.id}><td>{x.name}{x.hint && <span className="mono muted small"> · hw_…{x.hint}</span>}</td><td><span className={'pill ' + (x.readOnly ? 'off' : 'accent')}>{x.readOnly ? t('tokens.readOnlyShort') : t('tokens.full')}</span></td><td className="small muted">{formatTime(lang, x.createdAt)}</td><td className="small muted">{x.lastUsed ? formatAgo(lang, x.lastUsed) : t('users.never')}</td>
              <td style={{ textAlign: 'right' }}><ConfirmButton label={t('tokens.revoke')} onConfirm={() => run(async () => { await del(`/api/me/tokens/${x.id}`); load(); })} /></td></tr>
          ))}</tbody>
        </table></div>
      )}
    </section>
  );
}

function MQTTTab() {
  const t = useT();
  const [cfg, setCfg] = useState<MQTTSettings>({ enabled: false, broker: '', baseTopic: 'homewarden' });
  const [status, setStatus] = useState('');
  const [topic, setTopic] = useState('');
  const [creds, setCreds] = useState<Credential[]>([]);
  const { busy, error, run } = useAction();
  useEffect(() => {
    get<{ settings: MQTTSettings; status: string; commandTopic: string }>('/api/settings/mqtt').then((r) => { setCfg(r.settings); setStatus(r.status); setTopic(r.commandTopic); }).catch(() => {});
    get<Credential[]>('/api/credentials').then(setCreds).catch(() => {});
  }, []);
  return (
    <form className="card" style={{ maxWidth: 640 }} onSubmit={(e) => { e.preventDefault(); run(async () => {
      const r = await put<{ settings: MQTTSettings; status: string }>('/api/settings/mqtt', cfg);
      setCfg(r.settings); setTimeout(() => get<{ status: string }>('/api/settings/mqtt').then((x) => setStatus(x.status)), 1500);
    }); }}>
      <div className="card-head"><h2 className="grow">MQTT</h2><span className={'pill ' + (status === 'connected' ? 'ok' : 'off')}>{t('mqtt.status.' + status.split(':')[0]) || status}</span></div>
      <p className="small muted">{t('mqtt.intro')}</p>
      <label className="check"><input id="mqtt-enabled" type="checkbox" checked={cfg.enabled} onChange={(e) => setCfg({ ...cfg, enabled: e.target.checked })} />{t('mqtt.enabled')}</label>
      <label className="field">{t('mqtt.broker')}
        <input id="mqtt-broker" type="text" className="mono" value={cfg.broker} onChange={(e) => setCfg({ ...cfg, broker: e.target.value })} placeholder="tcp://192.168.90.20:1883" />
      </label>
      <label className="field">{t('backups.login')}
        <select id="mqtt-cred" value={cfg.credentialId ?? ''} onChange={(e) => setCfg({ ...cfg, credentialId: e.target.value ? Number(e.target.value) : undefined })}>
          <option value="">{t('backups.guest')}</option>
          {creds.filter((c) => c.type === 'password').map((c) => <option key={c.id} value={c.id}>{c.name} ({c.username})</option>)}
        </select>
      </label>
      <label className="check"><input id="mqtt-ha" type="checkbox" checked={!!cfg.haDiscovery} onChange={(e) => setCfg({ ...cfg, haDiscovery: e.target.checked })} />{t('mqtt.ha')}</label>
      <span className="small muted">{t('mqtt.haHint')}</span>
      <label className="field">{t('mqtt.base')}
        <input id="mqtt-base" type="text" className="mono" value={cfg.baseTopic} onChange={(e) => setCfg({ ...cfg, baseTopic: e.target.value })} />
      </label>
      <div className="small muted stack" style={{ gap: 4 }}>
        <span>{t('mqtt.topics')}</span>
        <code>{cfg.baseTopic}/device/&lt;device&gt;/state · /updates</code>
        <code>{topic || cfg.baseTopic + '/device/<device>/cmd'} ← scan | backup | update | update security</code>
      </div>
      <ErrorAlert error={error} />
      <div><button className="btn" type="submit" disabled={busy}>{t('common.save')}</button></div>
    </form>
  );
}

// After Backup and Busy rules, before Connection.
// Connection is the last card at this point (it is pushed first, the others are spliced in before it).
deviceSections.splice(Math.max(0, deviceSections.length - 1), 0, AutomationCard);
settingsTabs.push(
  { path: 'tokens', labelKey: 'settings.tokens', element: <TokensTab /> },
  { path: 'mqtt', labelKey: 'settings.mqtt', perm: 'settings.manage', element: <MQTTTab /> },
);
