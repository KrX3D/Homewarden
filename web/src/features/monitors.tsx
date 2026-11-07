// Uptime monitors, response-time charts, maintenance windows and the public status page.
import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { del, get, post, put, type Device, type Maintenance, type Monitor, type MonitorConfig, type MonitorPoint, type MonitorType } from '../api';
import { formatAgo, formatTime, useLang, useT } from '../i18n';
import { useSession } from '../session';
import { useEvents } from '../useEvents';
import { ConfirmButton, ErrorAlert, Modal, Segmented, Spinner, useAction } from '../components/ui';
import { IconEdit, IconPlus, IconPulse, Logo } from '../components/icons';
import { navItems, settingsTabs } from '../extensions';

const pct = (u: number) => (u < 0 ? '–' : (u * 100).toFixed(u === 1 ? 0 : 2) + '%');
const statusClass: Record<string, string> = { up: 'ok', down: 'crit', unknown: 'off' };

function target(m: Monitor) {
  const c = m.config;
  switch (m.type) {
    case 'http': return c.url ?? '';
    case 'tcp': return `${c.host}:${c.port}`;
    case 'ping': return c.host ?? '';
    case 'dns': return `${c.record || 'A'} ${c.host}`;
    case 'push': return 'push';
  }
}

/** Response-time line with failed checks marked; drawn to scale. */
function ResponseChart({ points, hours }: { points: MonitorPoint[]; hours: number }) {
  const lang = useLang();
  const W = 600, H = 120, P = 28;
  const now = Date.now() / 1000;
  const from = now - hours * 3600;
  const vals = points.filter((p) => p.ms != null).map((p) => p.ms as number);
  const top = Math.max(10, ...vals) * 1.15;
  const x = (at: number) => P + ((at - from) / (now - from)) * (W - P - 6);
  const y = (ms: number) => H - 18 - (ms / top) * (H - 30);
  const ok = points.filter((p) => p.ok && p.ms != null);
  const line = ok.map((p, i) => `${i ? 'L' : 'M'}${x(p.at).toFixed(1)},${y(p.ms as number).toFixed(1)}`).join(' ');
  const area = ok.length ? `${line} L${x(ok[ok.length - 1].at).toFixed(1)},${H - 18} L${x(ok[0].at).toFixed(1)},${H - 18} Z` : '';
  const ticks = [0, 0.5, 1].map((f) => Math.round(top * f));
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="chart" role="img" aria-label="response time">
      {ticks.map((v) => <g key={v}><line x1={P} x2={W} y1={y(v)} y2={y(v)} className="grid" /><text x={P - 4} y={y(v) + 3} textAnchor="end" className="axis">{v}</text></g>)}
      {area && <path d={area} className="area" />}
      {line && <path d={line} className="line" />}
      {points.filter((p) => !p.ok).map((p, i) => <rect key={i} x={x(p.at) - 1} y={10} width={2} height={H - 28} className="fail" />)}
      <text x={P} y={H - 4} className="axis">{new Date(from * 1000).toLocaleString(lang === 'de' ? 'de-DE' : 'en-GB', { hour: '2-digit', minute: '2-digit', day: hours > 24 ? '2-digit' : undefined, month: hours > 24 ? '2-digit' : undefined })}</text>
      <text x={W - 6} y={H - 4} textAnchor="end" className="axis">ms</text>
    </svg>
  );
}

function Bars({ points }: { points: MonitorPoint[] }) {
  // 48 half-hour buckets over 24 h: green all ok, red any failure, grey no data.
  const now = Date.now() / 1000;
  const buckets = Array.from({ length: 48 }, (_, i) => {
    const from = now - (48 - i) * 1800, to = from + 1800;
    const inB = points.filter((p) => p.at >= from && p.at < to);
    return inB.length === 0 ? 'none' : inB.every((p) => p.ok) ? 'ok' : 'crit';
  });
  return <div className="bars">{buckets.map((b, i) => <span key={i} className={'bar-' + b} />)}</div>;
}

function MonitorsPage() {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const [list, setList] = useState<Monitor[] | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [editing, setEditing] = useState<Monitor | 'new' | null>(null);
  const load = useCallback(() => { get<Monitor[]>('/api/monitors').then(setList).catch(() => {}); }, []);
  useEffect(() => { load(); }, [load]);
  useEvents({ 'monitor.state': load }, load);
  const manage = can('devices.manage');
  if (!list) return <Spinner />;
  const down = list.filter((m) => m.status === 'down').length;
  const sel = list.find((m) => m.id === selected) ?? null;
  return (
    <>
      <div className="page-head">
        <h1 className="grow">{t('mon.title')}</h1>
        {down > 0 && <span className="pill crit">{t('mon.downCount', { n: down })}</span>}
        {manage && <button className="btn" type="button" onClick={() => setEditing('new')}><IconPlus />{t('mon.add')}</button>}
      </div>
      {list.length === 0 ? (
        <div className="card empty"><IconPulse /><h2>{t('mon.emptyTitle')}</h2><p>{t('mon.emptyText')}</p></div>
      ) : (
        <div className="table-wrap">
          <table className="tbl">
            <thead><tr><th>{t('cred.name')}</th><th>{t('mon.target')}</th><th>{t('devices.colState')}</th><th>{t('mon.response')}</th><th>24h</th><th>30d</th><th>{t('mon.cert')}</th><th></th></tr></thead>
            <tbody>{list.map((m) => (
              <tr key={m.id} className="clickable" onClick={() => setSelected(m.id === selected ? null : m.id)}>
                <td><strong>{m.name}</strong>{!m.enabled && <span className="muted small"> ({t('common.off')})</span>}</td>
                <td className="mono small ellipsis" style={{ maxWidth: 260 }}>{target(m)}</td>
                <td><span className={'pill dot ' + (statusClass[m.status] ?? 'off')} title={m.lastMsg}>{t('mon.status.' + m.status)}</span></td>
                <td className="mono small">{m.lastMs != null ? `${m.lastMs.toFixed(0)} ms` : '–'}</td>
                <td className="mono small">{pct(m.uptime24h)}</td>
                <td className="mono small">{pct(m.uptime30d)}</td>
                <td className="small">{m.certExpires ? <span className={m.certExpires - Date.now() / 1000 < 14 * 86400 ? 'pill warn' : 'muted'}>{formatAgo(lang, m.certExpires)}</span> : ''}</td>
                <td style={{ textAlign: 'right' }}>{manage && <button className="btn subtle sm" type="button" aria-label={t('common.edit')} onClick={(e) => { e.stopPropagation(); setEditing(m); }}><IconEdit /></button>}</td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      )}
      {manage && <SuggestedMonitors monitors={list} onAdded={load} />}
      {sel && <MonitorDetail m={sel} />}
      <MaintenanceCard />
      {editing && <MonitorModal monitor={editing === 'new' ? null : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
    </>
  );
}

// ---------------------------------------------------------------- suggestions from devices

// Web interfaces of known integrations get an HTTP check (status and certificate);
// everything else the device's own ready check, or ping.
const WEB: Record<string, (h: string) => MonitorConfig> = {
  'home-assistant': (h) => ({ url: `http://${h}:8123/`, expectStatus: '200-399' }),
  octoprint: (h) => ({ url: `http://${h}/`, expectStatus: '200-399' }),
  proxmox: (h) => ({ url: `https://${h}:8006/`, expectStatus: '200-399', insecure: true }),
  'proxmox-backup': (h) => ({ url: `https://${h}:8007/`, expectStatus: '200-399', insecure: true }),
  unifi: (h) => ({ url: `https://${h}/`, expectStatus: '200-399', insecure: true }),
  deploydeck: (h) => ({ url: `http://${h}:8080/`, expectStatus: '200-399' }),
};

type Suggestion = { device: Device; type: MonitorType; config: MonitorConfig; label: string };

function suggestFor(d: Device): Suggestion | null {
  if (!d.host) return null;
  const web = WEB[d.kind];
  if (web) { const c = web(d.host); return { device: d, type: 'http', config: c, label: c.url! }; }
  const ready = d.presence?.ready;
  if (ready?.method === 'tcp' && ready.port) return { device: d, type: 'tcp', config: { host: d.host, port: ready.port }, label: `${d.host}:${ready.port}` };
  return { device: d, type: 'ping', config: { host: d.host }, label: `ping ${d.host}` };
}

const HIDDEN_KEY = 'hw.monitorSuggestions.hidden';
function readHidden(): number[] {
  try { return JSON.parse(localStorage.getItem(HIDDEN_KEY) || '[]'); } catch { return []; }
}

function SuggestedMonitors({ monitors, onAdded }: { monitors: Monitor[]; onAdded: () => void }) {
  const t = useT();
  const [devices, setDevices] = useState<Device[]>([]);
  const [hidden, setHidden] = useState<number[]>(readHidden);
  const [picked, setPicked] = useState<Record<number, boolean>>({});
  const { busy, error, run } = useAction();
  useEffect(() => { get<Device[]>('/api/devices?status=active').then(setDevices).catch(() => {}); }, []);
  const covered = new Set(monitors.map((m) => m.deviceId).filter((x): x is number => x != null));
  const list = devices.filter((d) => !covered.has(d.id) && !hidden.includes(d.id)).map(suggestFor).filter((s): s is Suggestion => !!s);
  if (list.length === 0) return null;
  // Online devices are preselected; ones that are off now (a printer Pi) would start "down".
  const isPicked = (s: Suggestion) => picked[s.device.id] ?? (s.device.state === 'ready' || s.device.state === 'reachable');
  const chosen = list.filter(isPicked);
  const hide = (ids: number[]) => {
    const next = [...hidden, ...ids];
    setHidden(next);
    try { localStorage.setItem(HIDDEN_KEY, JSON.stringify(next)); } catch { /* private mode */ }
  };
  const add = () => run(async () => {
    for (const s of chosen) {
      await post('/api/monitors', { name: s.device.name, type: s.type, config: { ...s.config, certDays: s.type === 'http' ? 14 : undefined }, interval: 60, enabled: true, public: false, deviceId: s.device.id });
    }
    onAdded();
  });
  return (
    <section className="card stack">
      <div className="card-head">
        <h2 className="grow">{t('mon.suggestTitle')}</h2>
        <button className="btn subtle sm" type="button" onClick={() => hide(list.map((s) => s.device.id))}>{t('mon.suggestHideAll')}</button>
      </div>
      <p className="small muted" style={{ margin: 0 }}>{t('mon.suggestIntro')}</p>
      <div className="table-wrap">
        <table className="tbl">
          <tbody>{list.map((s) => (
            <tr key={s.device.id}>
              <td style={{ width: 32 }}><input type="checkbox" aria-label={s.device.name} checked={isPicked(s)} onChange={(e) => setPicked({ ...picked, [s.device.id]: e.target.checked })} /></td>
              <td><strong>{s.device.name}</strong>{!(s.device.state === 'ready' || s.device.state === 'reachable') && <span className="muted small"> · {t('mon.suggestOffline')}</span>}</td>
              <td><span className="chip">{s.type.toUpperCase()}</span></td>
              <td className="mono small ellipsis" style={{ maxWidth: 320 }}>{s.label}</td>
              <td style={{ textAlign: 'right' }}><button className="btn subtle sm" type="button" onClick={() => hide([s.device.id])}>{t('mon.suggestHide')}</button></td>
            </tr>
          ))}</tbody>
        </table>
      </div>
      <ErrorAlert error={error} />
      <div><button className="btn" type="button" disabled={busy || chosen.length === 0} onClick={add}>{t('mon.suggestAdd', { n: chosen.length })}</button></div>
    </section>
  );
}

function MonitorDetail({ m }: { m: Monitor }) {
  const t = useT();
  const lang = useLang();
  const [hours, setHours] = useState<'24' | '168' | '720'>('24');
  const [points, setPoints] = useState<MonitorPoint[]>([]);
  const load = useCallback(() => { get<MonitorPoint[]>(`/api/monitors/${m.id}/results?hours=${hours}`).then(setPoints).catch(() => {}); }, [m.id, hours]);
  useEffect(() => { load(); }, [load]);
  useEvents({ 'monitor.result': (e: { id: number }) => e.id === m.id && load() });
  const okMs = points.filter((p) => p.ok && p.ms != null).map((p) => p.ms as number);
  const avg = okMs.length ? okMs.reduce((a, b) => a + b, 0) / okMs.length : null;
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="grow">{m.name}</h2>
        <Segmented label={t('mon.range')} value={hours} onChange={setHours} options={[{ value: '24', label: '24h' }, { value: '168', label: '7d' }, { value: '720', label: '30d' }]} />
      </div>
      <ResponseChart points={points} hours={Number(hours)} />
      <div className="row small muted" style={{ gap: 18 }}>
        <span>{t('mon.avg')}: <span className="mono">{avg != null ? avg.toFixed(0) + ' ms' : '–'}</span></span>
        <span>{t('mon.last')}: {m.lastMsg || '–'}</span>
        {m.statusSince && <span>{t('detail.since')}: {formatTime(lang, m.statusSince)}</span>}
      </div>
      {m.type === 'push' && m.pushToken && (
        <div className="stack small" style={{ gap: 4 }}>
          <span>{t('mon.pushHint')}</span>
          <code style={{ overflowWrap: 'anywhere' }}>{location.origin}/api/push/{m.pushToken}?status=up&amp;msg=OK</code>
        </div>
      )}
    </section>
  );
}

const TYPES: MonitorType[] = ['http', 'tcp', 'ping', 'dns', 'push'];

function MonitorModal({ monitor, onClose, onSaved }: { monitor: Monitor | null; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const [name, setName] = useState(monitor?.name ?? '');
  const [type, setType] = useState<MonitorType>(monitor?.type ?? 'http');
  const [cfg, setCfg] = useState<MonitorConfig>(monitor?.config ?? { certDays: 14 });
  const [interval, setInterval] = useState(monitor?.interval ?? 60);
  const [enabled, setEnabled] = useState(monitor?.enabled ?? true);
  const [pub, setPub] = useState(monitor?.public ?? false);
  const [deviceId, setDeviceId] = useState<number | null>(monitor?.deviceId ?? null);
  const [devices, setDevices] = useState<Device[]>([]);
  const [test, setTest] = useState<{ ok: boolean; ms: number; msg: string } | null>(null);
  const { busy, error, run } = useAction();
  useEffect(() => { get<Device[]>('/api/devices?status=active').then(setDevices).catch(() => {}); }, []);
  const set = (k: keyof MonitorConfig, v: unknown) => { setTest(null); setCfg((c) => ({ ...c, [k]: v })); };
  const body = () => ({ name, type, config: cfg, interval, enabled, public: pub, deviceId });
  const save = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      if (monitor) await put(`/api/monitors/${monitor.id}`, body());
      else await post('/api/monitors', body());
      onSaved();
    });
  };
  const text = (k: keyof MonitorConfig, label: string, ph = '', mono = true) => (
    <label className="field" key={k}>{label}
      <input id={'mon-' + k} type="text" className={mono ? 'mono' : ''} value={String(cfg[k] ?? '')} placeholder={ph} onChange={(e) => set(k, e.target.value)} />
    </label>
  );
  return (
    <Modal title={monitor ? t('mon.edit') : t('mon.add')} onClose={onClose} footer={
      <>
        {monitor && <ConfirmButton label={t('common.delete')} onConfirm={() => run(async () => { await del(`/api/monitors/${monitor.id}`); onSaved(); })} />}
        <span style={{ flex: 1 }} />
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        {type !== 'push' && <button className="btn ghost" type="button" disabled={busy} onClick={() => run(async () => setTest(await post('/api/monitors/test', body())))}>{t('mon.test')}</button>}
        <button className="btn" type="submit" form="mon-form" disabled={busy}>{t('common.save')}</button>
      </>
    }>
      <form id="mon-form" className="stack" onSubmit={save}>
        <div className="row">
          <label className="field" style={{ flex: '2 1 200px' }}>{t('cred.name')}
            <input id="mon-name" type="text" value={name} onChange={(e) => setName(e.target.value)} required placeholder="Gitea" />
          </label>
          <label className="field" style={{ flex: '1 1 120px' }}>{t('cred.type')}
            <select id="mon-type" value={type} onChange={(e) => { setType(e.target.value as MonitorType); setTest(null); }} disabled={!!monitor}>
              {TYPES.map((x) => <option key={x} value={x}>{t('monType.' + x)}</option>)}
            </select>
          </label>
        </div>
        {type === 'http' && (
          <>
            {text('url', 'URL', 'https://gitea.krx3d.de/')}
            <div className="row">
              <div style={{ flex: '1 1 140px' }}>{text('expectStatus', t('mon.expectStatus'), '200-399')}</div>
              <div style={{ flex: '2 1 200px' }}>{text('keyword', t('mon.keyword'), '', false)}</div>
            </div>
            <label className="check"><input type="checkbox" checked={!!cfg.invertKeyword} onChange={(e) => set('invertKeyword', e.target.checked)} />{t('mon.invert')}</label>
            <div className="row">
              <div style={{ flex: '1 1 160px' }}>{text('jsonPath', t('mon.jsonPath'), 'status.health')}</div>
              <div style={{ flex: '1 1 140px' }}>{text('jsonExpect', t('mon.jsonExpect'), 'ok')}</div>
            </div>
            <div className="row" style={{ alignItems: 'flex-end' }}>
              <label className="field" style={{ flex: '0 1 200px' }}>{t('mon.certDays')}
                <input type="number" min={0} value={cfg.certDays ?? 14} onChange={(e) => set('certDays', Number(e.target.value))} />
              </label>
              <label className="check"><input type="checkbox" checked={!!cfg.insecure} onChange={(e) => set('insecure', e.target.checked)} />{t('mon.insecure')}</label>
            </div>
          </>
        )}
        {(type === 'tcp' || type === 'ping' || type === 'dns') && (
          <div className="row">
            <div style={{ flex: '2 1 200px' }}>{text('host', type === 'dns' ? t('mon.dnsName') : t('deviceForm.host'), type === 'dns' ? 'gitea.krx3d.de' : '192.168.90.1')}</div>
            {type === 'tcp' && <label className="field" style={{ flex: '0 1 100px' }}>{t('deviceForm.port')}<input type="number" value={cfg.port ?? ''} onChange={(e) => set('port', Number(e.target.value))} /></label>}
            {type === 'dns' && (
              <label className="field" style={{ flex: '0 1 110px' }}>{t('mon.record')}
                <select value={cfg.record || 'A'} onChange={(e) => set('record', e.target.value)}>{['A', 'AAAA', 'CNAME', 'MX', 'TXT'].map((r) => <option key={r}>{r}</option>)}</select>
              </label>
            )}
          </div>
        )}
        {type === 'dns' && <div className="row"><div style={{ flex: 1 }}>{text('server', t('mon.dnsServer'), '192.168.90.55')}</div><div style={{ flex: 1 }}>{text('expect', t('mon.dnsExpect'), '192.168.90.')}</div></div>}
        {type === 'push' && <p className="small muted">{t('mon.pushIntro')}</p>}
        <div className="row" style={{ alignItems: 'flex-end' }}>
          <label className="field" style={{ flex: '0 1 160px' }}>{t('deviceForm.interval')}
            <input type="number" min={20} value={interval} onChange={(e) => setInterval(Number(e.target.value))} />
          </label>
          <label className="field" style={{ flex: '1 1 200px' }}>{t('mon.device')}
            <select value={deviceId ?? ''} onChange={(e) => setDeviceId(e.target.value ? Number(e.target.value) : null)}>
              <option value="">–</option>
              {devices.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
            </select>
          </label>
        </div>
        <label className="check"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />{t('notify.enabled')}</label>
        <label className="check"><input type="checkbox" checked={pub} onChange={(e) => setPub(e.target.checked)} />{t('mon.public')}</label>
        {test && <div className={'alert ' + (test.ok ? 'ok' : 'error')}>{test.ok ? t('mon.testOk', { ms: test.ms.toFixed(0) }) : t('mon.testFail')}: {test.msg}</div>}
        <ErrorAlert error={error} />
      </form>
    </Modal>
  );
}

function toLocalInput(unix: number) {
  const d = new Date(unix * 1000);
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

function MaintenanceCard() {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const [list, setList] = useState<Maintenance[]>([]);
  const [devices, setDevices] = useState<Device[]>([]);
  const now = Math.floor(Date.now() / 1000);
  const [form, setForm] = useState({ title: '', deviceId: '', starts: toLocalInput(now), ends: toLocalInput(now + 3600) });
  const { busy, error, run } = useAction();
  const load = () => get<Maintenance[]>('/api/maintenance').then(setList).catch(() => {});
  useEffect(() => { load(); get<Device[]>('/api/devices?status=active').then(setDevices).catch(() => {}); }, []);
  const name = (id: number | null) => (id ? devices.find((d) => d.id === id)?.name ?? '?' : t('maint.all'));
  return (
    <section className="card">
      <h2>{t('maint.title')}</h2>
      <p className="small muted">{t('maint.intro')}</p>
      {list.map((m) => (
        <div className="row" key={m.id}>
          <span className={'pill ' + (m.starts <= now ? 'warn' : 'off')}>{m.starts <= now ? t('maint.active') : t('maint.planned')}</span>
          <strong>{m.title}</strong><span className="small muted">{name(m.deviceId)} · {formatTime(lang, m.starts)} – {formatTime(lang, m.ends)}</span>
          <span style={{ flex: 1 }} />
          {can('devices.manage') && <ConfirmButton label={t('common.delete')} onConfirm={() => run(async () => { await del(`/api/maintenance/${m.id}`); load(); })} />}
        </div>
      ))}
      {can('devices.manage') && (
        <form className="row" style={{ alignItems: 'flex-end' }} onSubmit={(e) => { e.preventDefault(); run(async () => {
          await post('/api/maintenance', { title: form.title, deviceId: form.deviceId ? Number(form.deviceId) : null,
            starts: Math.floor(new Date(form.starts).getTime() / 1000), ends: Math.floor(new Date(form.ends).getTime() / 1000) });
          setForm({ ...form, title: '' }); load();
        }); }}>
          <label className="field" style={{ flex: '2 1 160px' }}>{t('maint.what')}<input type="text" value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} required placeholder={t('maint.ph')} /></label>
          <label className="field" style={{ flex: '1 1 140px' }}>{t('mon.device')}
            <select value={form.deviceId} onChange={(e) => setForm({ ...form, deviceId: e.target.value })}>
              <option value="">{t('maint.all')}</option>{devices.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
            </select>
          </label>
          <label className="field" style={{ flex: '1 1 170px' }}>{t('maint.from')}<input type="datetime-local" value={form.starts} onChange={(e) => setForm({ ...form, starts: e.target.value })} /></label>
          <label className="field" style={{ flex: '1 1 170px' }}>{t('maint.to')}<input type="datetime-local" value={form.ends} onChange={(e) => setForm({ ...form, ends: e.target.value })} /></label>
          <button className="btn ghost" type="submit" disabled={busy}><IconPlus />{t('maint.add')}</button>
        </form>
      )}
      <ErrorAlert error={error} />
    </section>
  );
}

function StatusPageTab() {
  const t = useT();
  const [sp, setSp] = useState({ enabled: false, title: 'Status' });
  const [ok, setOk] = useState(false);
  const { busy, error, run } = useAction();
  useEffect(() => { get<typeof sp>('/api/settings/status-page').then(setSp).catch(() => {}); }, []);
  return (
    <form className="card" style={{ maxWidth: 620 }} onSubmit={(e) => { e.preventDefault(); setOk(false); run(async () => { await put('/api/settings/status-page', sp); setOk(true); }); }}>
      <h2>{t('status.title')}</h2>
      <p className="small muted">{t('status.intro')}</p>
      <label className="check"><input type="checkbox" checked={sp.enabled} onChange={(e) => setSp({ ...sp, enabled: e.target.checked })} />{t('status.enabled')}</label>
      <label className="field">{t('status.pageTitle')}<input type="text" value={sp.title} onChange={(e) => setSp({ ...sp, title: e.target.value })} /></label>
      {sp.enabled && <a href="/status" target="_blank" rel="noreferrer" className="small">{location.origin}/status</a>}
      {ok && <div className="alert ok">{t('conn.saved')}</div>}
      <ErrorAlert error={error} />
      <div><button className="btn" type="submit" disabled={busy}>{t('common.save')}</button></div>
    </form>
  );
}

/** Public status page at /status (no login). */
export function PublicStatus() {
  const t = useT();
  const [data, setData] = useState<{ title: string; monitors: { name: string; status: string; uptime24h: number; uptime30d: number; bars: MonitorPoint[] }[]; maintenance: string[] } | null>(null);
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    const load = () => get<typeof data>('/api/public/status').then(setData).catch(() => setMissing(true));
    load();
    const i = setInterval(load, 60000);
    return () => clearInterval(i);
  }, []);
  if (missing) return <div className="auth-wrap"><p className="muted">{t('status.off')}</p></div>;
  if (!data) return <div className="auth-wrap"><Spinner /></div>;
  const allUp = data.monitors.every((m) => m.status === 'up');
  return (
    <div className="status-page">
      <header className="row"><Logo className="status-logo" /><h1>{data.title}</h1></header>
      <div className={'alert ' + (allUp ? 'ok' : 'error')}>{allUp ? t('status.allUp') : t('status.someDown')}</div>
      {data.maintenance.map((m) => <div key={m} className="alert warn">{t('status.maintenance')}: {m}</div>)}
      <div className="stack">
        {data.monitors.map((m) => (
          <div className="card" key={m.name}>
            <div className="card-head"><h3 className="grow">{m.name}</h3><span className="mono small muted">{pct(m.uptime30d)} 30d</span><span className={'pill dot ' + (statusClass[m.status] ?? 'off')}>{t('mon.status.' + m.status)}</span></div>
            <Bars points={m.bars} />
          </div>
        ))}
      </div>
    </div>
  );
}

navItems.splice(Math.min(1, navItems.length), 0, { path: 'monitors', labelKey: 'nav.monitors', perm: 'devices.view', icon: <IconPulse />, element: <MonitorsPage /> });
settingsTabs.push({ path: 'status-page', labelKey: 'settings.statusPage', perm: 'settings.manage', element: <StatusPageTab /> });
