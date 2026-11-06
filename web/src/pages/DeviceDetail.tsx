import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { del, get, post, put, type Device } from '../api';
import { formatAgo, formatTime, useLang, useT } from '../i18n';
import { useSession } from '../session';
import { useEvents } from '../useEvents';
import { ConfirmButton, ErrorAlert, Spinner, useAction } from '../components/ui';
import { IconEdit, IconRefresh } from '../components/icons';
import { DeviceFormModal, draftFrom, type DeviceDraft } from '../components/DeviceForm';
import { StatePill, hasPresence } from '../components/deviceBits';
import { deviceSections } from '../extensions';

type History = { since: number; now: number; events: { at: number; state: Device['state'] }[] };

export default function DeviceDetail() {
  const t = useT();
  const lang = useLang();
  const { id } = useParams();
  const nav = useNavigate();
  const { can } = useSession();
  const [d, setD] = useState<Device | null>(null);
  const [parents, setParents] = useState<Device[]>([]);
  const [history, setHistory] = useState<History | null>(null);
  const [editing, setEditing] = useState(false);
  const { error, run, busy } = useAction();
  const edit = useAction();
  const manage = can('devices.manage');

  const load = useCallback(async () => {
    try {
      const [dev, hist] = await Promise.all([get<Device>(`/api/devices/${id}`), get<History>(`/api/devices/${id}/presence?hours=24`)]);
      setD(dev);
      setHistory(hist);
    } catch (e) {
      run(() => Promise.reject(e));
    }
  }, [id]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { load(); }, [load]);
  useEffect(() => { if (editing) get<Device[]>('/api/devices?status=active').then(setParents).catch(() => {}); }, [editing]);
  useEvents({
    'device.state': (e: { id: number }) => { if (String(e.id) === id) load(); },
    'device.seen': (e: { id: number; at: number }) => { if (String(e.id) === id) setD((cur) => (cur ? { ...cur, lastSeen: e.at } : cur)); },
    'device.changed': (e: { id: number }) => { if (String(e.id) === id) load(); },
  }, load);

  if (!d) return error ? <ErrorAlert error={error} /> : <Spinner />;

  const save = (draft: DeviceDraft) => edit.run(async () => {
    setD(await put<Device>(`/api/devices/${d.id}`, draft));
    setEditing(false);
  });

  const check = () => run(async () => { setD(await post<Device>(`/api/devices/${d.id}/check`)); load(); });

  return (
    <>
      <div className="page-head">
        <div className="grow stack" style={{ gap: 4 }}>
          <Link to="/devices" className="small">{t('detail.back')}</Link>
          <div className="row"><h1>{d.name}</h1><StatePill state={d.state} /></div>
        </div>
        {hasPresence(d) && <button className="btn ghost" type="button" onClick={check} disabled={busy}><IconRefresh />{t('detail.checkNow')}</button>}
        {manage && <button className="btn ghost" type="button" onClick={() => setEditing(true)}><IconEdit />{t('common.edit')}</button>}
      </div>
      <ErrorAlert error={error} />

      {d.status !== 'active' && <div className="alert warn">{d.status === 'discovered' ? t('detail.inInbox') : t('detail.isIgnored')}</div>}

      <div className="grid-2">
        <section className="card">
          <h2>{t('detail.overview')}</h2>
          <dl className="kv">
            <dt>{t('devices.colHost')}</dt><dd className="mono">{d.host || '–'}</dd>
            <dt>{t('devices.colGroup')}</dt><dd>{d.groupName || '–'}</dd>
            <dt>{t('detail.state')}</dt><dd>{t('stateHint.' + d.state)}</dd>
            <dt>{t('detail.since')}</dt><dd>{d.stateSince ? `${formatTime(lang, d.stateSince)} (${formatAgo(lang, d.stateSince)})` : '–'}</dd>
            <dt>{t('devices.colSeen')}</dt><dd>{d.lastSeen ? formatAgo(lang, d.lastSeen) : t('devices.neverSeen')}</dd>
            {d.rttMs != null && <><dt>{t('detail.rtt')}</dt><dd className="mono">{d.rttMs.toFixed(1)} ms</dd></>}
            <dt>{t('detail.checks')}</dt><dd>{describePresence(t, d)}</dd>
          </dl>
          {d.notes && <p className="small" style={{ whiteSpace: 'pre-wrap' }}>{d.notes}</p>}
        </section>

        <section className="card">
          <h2>{t('detail.last24h')}</h2>
          {history && <Timeline h={history} />}
          <div className="row small muted" style={{ gap: 14 }}>
            <Legend cls="ok" label={t('state.ready')} />
            <Legend cls="warn" label={t('state.reachable')} />
            <Legend cls="off" label={t('state.offline')} />
          </div>
          {history && uptime(history) !== '–' && <p className="small muted">{t('detail.uptime', { pct: uptime(history) })}</p>}
        </section>
      </div>

      {deviceSections.map((S, i) => <S key={i} device={d} />)}

      {manage && (
        <section className="card">
          <h2>{t('detail.manage')}</h2>
          <div className="row">
            {d.status === 'active' && <ConfirmButton label={t('detail.ignore')} onConfirm={() => run(async () => { await post(`/api/devices/${d.id}/status`, { status: 'ignored' }); nav('/devices'); })} />}
            {d.status !== 'active' && <button className="btn" type="button" onClick={() => run(async () => { setD(await post<Device>(`/api/devices/${d.id}/status`, { status: 'active' })); })}>{t('inbox.add')}</button>}
            <ConfirmButton label={t('detail.delete')} onConfirm={() => run(async () => { await del(`/api/devices/${d.id}`); nav('/devices'); })} />
          </div>
          <p className="small muted">{t('detail.manageHint')}</p>
        </section>
      )}

      {editing && <DeviceFormModal title={t('detail.editTitle', { name: d.name })} initial={draftFrom(d)} parents={parents.filter((p) => p.id !== d.id)} onClose={() => setEditing(false)} onSubmit={save} busy={edit.busy} error={edit.error} />}
    </>
  );
}

function describePresence(t: (k: string, v?: Record<string, string | number>) => string, d: Device) {
  const p = d.presence;
  if (p.reach.method === 'none' || !d.host) return t('detail.noChecks');
  const probe = (x: Device['presence']['reach']) => x.method === 'tcp' ? `TCP ${x.port}` : x.method === 'http' ? (x.url || 'HTTP') : t('probe.' + x.method);
  const parts = [t('detail.reachBy', { probe: probe(p.reach) })];
  if (p.ready.method !== 'none') parts.push(t('detail.readyBy', { probe: probe(p.ready), settle: p.settleSeconds }));
  parts.push(t('detail.every', { s: p.intervalSeconds }));
  return parts.join(' · ');
}

function Legend({ cls, label }: { cls: string; label: string }) {
  return <span className="row" style={{ gap: 5 }}><span className={'swatch ' + cls} />{label}</span>;
}

function segments(h: History) {
  const out: { from: number; to: number; state: string }[] = [];
  let cur = 'unknown';
  let from = h.since;
  for (const e of h.events) {
    if (e.at <= h.since) { cur = e.state; continue; }
    out.push({ from, to: e.at, state: cur });
    cur = e.state;
    from = e.at;
  }
  out.push({ from, to: h.now, state: cur });
  return out.filter((s) => s.to > s.from);
}

function uptime(h: History) {
  const segs = segments(h);
  const known = segs.filter((s) => s.state !== 'unknown');
  const total = known.reduce((a, s) => a + (s.to - s.from), 0);
  if (total < 60) return '–';
  const up = known.filter((s) => s.state === 'ready' || s.state === 'reachable').reduce((a, s) => a + (s.to - s.from), 0);
  return ((up / total) * 100).toFixed(1);
}

function Timeline({ h }: { h: History }) {
  const lang = useLang();
  const span = h.now - h.since;
  const cls: Record<string, string> = { ready: 'ok', reachable: 'warn', offline: 'off', unknown: 'none', paused: 'none' };
  const ticks = [0, 6, 12, 18, 24].map((hh) => h.since + hh * 3600);
  return (
    <div>
      <div className="timeline" role="img" aria-label="24h presence">
        {segments(h).map((s, i) => (
          <span key={i} className={'seg-' + (cls[s.state] ?? 'none')} style={{ left: `${((s.from - h.since) / span) * 100}%`, width: `${((s.to - s.from) / span) * 100}%` }}
            title={`${s.state}: ${formatTime(lang, s.from)} – ${formatTime(lang, s.to)}`} />
        ))}
      </div>
      <div className="timeline-axis small muted">
        {ticks.map((x) => <span key={x}>{new Date(x * 1000).toLocaleTimeString(lang === 'de' ? 'de-DE' : 'en-GB', { hour: '2-digit', minute: '2-digit' })}</span>)}
      </div>
    </div>
  );
}
