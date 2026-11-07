import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react';
import { Link, NavLink, Route, Routes, useNavigate } from 'react-router-dom';
import { get, post, put, type Device, type DeviceStatus, type Integration } from '../api';
import { formatAgo, useLang, useT } from '../i18n';
import { useSession } from '../session';
import { useEvents } from '../useEvents';
import { ErrorAlert, Modal, Segmented, useAction } from '../components/ui';
import { IconCards, IconChevron, IconDevices, IconInbox, IconPlus, IconTable, IconTrash, IconTree } from '../components/icons';
import { DeviceFormModal, draftFrom, type DeviceDraft } from '../components/DeviceForm';
import { StatePill } from '../components/deviceBits';
import DeviceDetail from './DeviceDetail';

export default function DevicesPage() {
  return (
    <Routes>
      <Route index element={<DeviceList status="active" />} />
      <Route path="inbox" element={<DeviceList status="discovered" />} />
      <Route path="ignored" element={<DeviceList status="ignored" />} />
      <Route path=":id" element={<DeviceDetail />} />
    </Routes>
  );
}

/** Loads devices of one status and keeps them live via server events. */
export function useDevices(status: DeviceStatus | 'all') {
  const [devices, setDevices] = useState<Device[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const load = useCallback(async () => {
    try {
      setDevices(await get<Device[]>(`/api/devices?status=${status}`));
      setError(null);
    } catch (e) {
      setError(e);
    } finally {
      setLoaded(true);
    }
  }, [status]);
  useEffect(() => { load(); }, [load]);
  useEvents({
    'device.state': (e: { id: number; state: Device['state']; since: number }) =>
      setDevices((cur) => cur.map((d) => (d.id === e.id ? { ...d, state: e.state, stateSince: e.since, lastSeen: e.state === 'offline' ? d.lastSeen : e.since } : d))),
    'device.seen': (e: { id: number; at: number }) => setDevices((cur) => cur.map((d) => (d.id === e.id ? { ...d, lastSeen: e.at } : d))),
    'device.changed': () => load(),
    'discovery.done': () => load(),
  }, load);
  return { devices, loaded, error, reload: load };
}

function DeviceList({ status }: { status: DeviceStatus }) {
  const t = useT();
  const { can, prefs, setPrefs } = useSession();
  const { devices, loaded, error, reload } = useDevices(status);
  const { devices: inbox } = useDevices('discovered');
  const [all, setAll] = useState<Device[]>([]);
  const [query, setQuery] = useState('');
  const [adding, setAdding] = useState(false);
  const [scanning, setScanning] = useState(false);
  const add = useAction();
  const view = prefs.deviceView ?? 'cards';
  const manage = can('devices.manage');

  useEffect(() => { if (adding) get<Device[]>('/api/devices?status=active').then(setAll).catch(() => {}); }, [adding]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return devices;
    return devices.filter((d) => [d.name, d.host, d.groupName, d.kind].some((x) => x.toLowerCase().includes(q)));
  }, [devices, query]);

  const create = (d: DeviceDraft) => add.run(async () => {
    await post<Device>('/api/devices', d);
    setAdding(false);
    reload();
  });

  const titles: Record<DeviceStatus, string> = { active: t('devices.title'), discovered: t('devices.inbox'), ignored: t('devices.ignored') };

  return (
    <>
      <div className="page-head">
        <h1 className="grow">{titles[status]}</h1>
        {status === 'active' && (
          <Segmented iconsOnly label={t('devices.layout')} value={view} onChange={(v) => setPrefs({ deviceView: v })} options={[
            { value: 'cards', label: t('devices.viewCards'), icon: <IconCards /> },
            { value: 'table', label: t('devices.viewTable'), icon: <IconTable /> },
            { value: 'tree', label: t('devices.viewTree'), icon: <IconTree /> },
          ]} />
        )}
        {manage && <button className="btn ghost" type="button" onClick={() => setScanning(true)}>{t('discovery.scan')}</button>}
        {manage && status === 'active' && <button className="btn" type="button" onClick={() => setAdding(true)}><IconPlus />{t('devices.add')}</button>}
      </div>

      <nav className="tabs">
        <NavLink to="/devices" end>{t('devices.title')}</NavLink>
        {manage && <NavLink to="/devices/inbox">{t('devices.inbox')}{inbox.length > 0 && <span className="nav-badge" style={{ marginLeft: 6 }}>{inbox.length}</span>}</NavLink>}
        {manage && <NavLink to="/devices/ignored">{t('devices.ignored')}</NavLink>}
      </nav>

      {devices.length > 6 && (
        <input id="device-search" type="text" placeholder={t('devices.search')} value={query} onChange={(e) => setQuery(e.target.value)} style={{ maxWidth: 360 }} />
      )}
      <ErrorAlert error={error} />

      {loaded && devices.length === 0 ? (
        <EmptyState status={status} onAdd={() => setAdding(true)} onScan={() => setScanning(true)} manage={manage} />
      ) : status !== 'active' ? (
        <InboxTable devices={filtered} status={status} onChanged={reload} />
      ) : view === 'table' ? (
        <DeviceTable devices={filtered} />
      ) : view === 'tree' ? (
        <DeviceTree devices={filtered} />
      ) : (
        <DeviceCards devices={filtered} />
      )}

      {adding && <DeviceFormModal title={t('devices.add')} initial={draftFrom()} parents={all} onClose={() => setAdding(false)} onSubmit={create} busy={add.busy} error={add.error} />}
      {scanning && <ScanModal onClose={() => { setScanning(false); reload(); }} />}
    </>
  );
}

function EmptyState({ status, onAdd, onScan, manage }: { status: DeviceStatus; onAdd: () => void; onScan: () => void; manage: boolean }) {
  const t = useT();
  if (status === 'discovered') {
    return (
      <div className="card empty"><IconInbox /><h2>{t('inbox.emptyTitle')}</h2><p>{t('inbox.emptyText')}</p>
        {manage && <button className="btn" type="button" onClick={onScan}>{t('discovery.scan')}</button>}</div>
    );
  }
  if (status === 'ignored') {
    return <div className="card empty"><IconInbox /><h2>{t('ignored.emptyTitle')}</h2><p>{t('ignored.emptyText')}</p></div>;
  }
  return (
    <div className="card empty"><IconDevices /><h2>{t('devices.emptyTitle')}</h2><p>{t('devices.emptyText')}</p>
      {manage && <div className="row" style={{ justifyContent: 'center' }}>
        <button className="btn" type="button" onClick={onAdd}><IconPlus />{t('devices.add')}</button>
        <button className="btn ghost" type="button" onClick={onScan}>{t('discovery.scan')}</button>
      </div>}
    </div>
  );
}

function LastSeen({ d }: { d: Device }) {
  const lang = useLang();
  const t = useT();
  if (d.state === 'ready' || d.state === 'reachable') return <span className="muted small">{d.rttMs != null ? `${d.rttMs.toFixed(1)} ms` : t('devices.online')}</span>;
  if (!d.lastSeen) return <span className="muted small">{t('devices.neverSeen')}</span>;
  return <span className="muted small">{t('devices.lastSeen', { ago: formatAgo(lang, d.lastSeen) })}</span>;
}

function DeviceCards({ devices }: { devices: Device[] }) {
  const nav = useNavigate();
  return (
    <div className="device-grid">
      {devices.map((d) => (
        <button key={d.id} type="button" className="device-card" onClick={() => nav(`/devices/${d.id}`)}>
          <div className="row" style={{ justifyContent: 'space-between', flexWrap: 'nowrap' }}>
            <strong className="ellipsis">{d.name}</strong>
            <StatePill state={d.state} />
          </div>
          <span className="mono muted small ellipsis">{d.host || ' '}</span>
          <div className="row" style={{ justifyContent: 'space-between' }}>
            <LastSeen d={d} />
            {d.groupName && <span className="chip">{d.groupName}</span>}
          </div>
        </button>
      ))}
    </div>
  );
}

function DeviceTable({ devices }: { devices: Device[] }) {
  const t = useT();
  const nav = useNavigate();
  return (
    <div className="table-wrap">
      <table className="tbl">
        <thead><tr><th>{t('devices.colName')}</th><th>{t('devices.colHost')}</th><th>{t('devices.colGroup')}</th><th>{t('devices.colState')}</th><th>{t('devices.colSeen')}</th></tr></thead>
        <tbody>
          {devices.map((d) => (
            <tr key={d.id} className="clickable" onClick={() => nav(`/devices/${d.id}`)}>
              <td><strong>{d.name}</strong></td>
              <td className="mono small">{d.host}</td>
              <td>{d.groupName}</td>
              <td><StatePill state={d.state} /></td>
              <td><LastSeen d={d} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function DeviceTree({ devices }: { devices: Device[] }) {
  const t = useT();
  const [closed, setClosed] = useState<Record<string, boolean>>({});
  const ids = new Set(devices.map((d) => d.id));
  const children = new Map<number, Device[]>();
  for (const d of devices) if (d.parentId && ids.has(d.parentId)) children.set(d.parentId, [...(children.get(d.parentId) ?? []), d]);
  const roots = devices.filter((d) => !d.parentId || !ids.has(d.parentId));
  const groups = new Map<string, Device[]>();
  for (const d of roots) groups.set(d.groupName, [...(groups.get(d.groupName) ?? []), d]);
  const groupNames = [...groups.keys()].sort((a, b) => (a === '' ? 1 : b === '' ? -1 : a.localeCompare(b)));
  const toggle = (k: string) => setClosed((c) => ({ ...c, [k]: !c[k] }));

  const row = (d: Device, depth: number): ReactNode => {
    const kids = children.get(d.id) ?? [];
    const key = 'd' + d.id;
    return (
      <div key={d.id}>
        <div className="tree-row" style={{ paddingLeft: 12 + depth * 22 }}>
          {kids.length > 0 ? <button type="button" className="btn subtle sm tree-toggle" onClick={() => toggle(key)} aria-label={t('devices.toggle')}><IconChevron open={!closed[key]} /></button> : <span className="tree-toggle" />}
          <Link to={`/devices/${d.id}`} className="tree-name"><strong>{d.name}</strong> <span className="mono muted small">{d.host}</span></Link>
          {kids.length > 0 && <span className="muted small">{t('devices.children', { n: kids.length })}</span>}
          <span style={{ flex: 1 }} />
          <LastSeen d={d} />
          <StatePill state={d.state} />
        </div>
        {!closed[key] && kids.map((k) => row(k, depth + 1))}
      </div>
    );
  };

  return (
    <div className="stack">
      {groupNames.map((g) => (
        <section key={g || '_'} className="tree">
          <button type="button" className="tree-group" onClick={() => toggle('g' + g)}>
            <IconChevron open={!closed['g' + g]} />
            <span>{g || t('devices.ungrouped')}</span>
            <span className="muted small">{groups.get(g)!.length}</span>
          </button>
          {!closed['g' + g] && groups.get(g)!.map((d) => row(d, 0))}
        </section>
      ))}
    </div>
  );
}

function InboxTable({ devices, status, onChanged }: { devices: Device[]; status: DeviceStatus; onChanged: () => void }) {
  const t = useT();
  const lang = useLang();
  const { error, run } = useAction();
  const [integs, setIntegs] = useState<Integration[]>([]);
  const [kinds, setKinds] = useState<Record<number, string>>({});
  useEffect(() => { if (status === 'discovered') get<Integration[]>('/api/integrations').then(setIntegs).catch(() => {}); }, [status]);
  const setStatus = (d: Device, s: DeviceStatus) => run(async () => {
    // Adding "as" an integration sets its type and online detection in one go; shared
    // settings then come from the integration setup.
    const kind = kinds[d.id];
    if (s === 'active' && kind) await put(`/api/devices/${d.id}/integration`, { kind, settings: {}, applyPresence: true });
    await post(`/api/devices/${d.id}/status`, { status: s });
    onChanged();
  });
  return (
    <>
      <p className="muted">{status === 'discovered' ? t('inbox.intro') : t('ignored.intro')}</p>
      <ErrorAlert error={error} />
      <div className="table-wrap">
        <table className="tbl">
          <thead><tr><th>{t('devices.colName')}</th><th>{t('devices.colHost')}</th><th>{t('devices.colMac')}</th><th>{t('inbox.source')}</th><th>{t('inbox.found')}</th><th></th></tr></thead>
          <tbody>
            {devices.map((d) => (
              <tr key={d.id}>
                <td><strong>{d.name}</strong></td>
                <td className="mono small">{d.host}</td>
                <td className="small">{d.mac ? <><span className="mono">{d.mac}</span>{d.vendor && <div className="muted">{d.vendor}</div>}</> : ''}</td>
                <td><span className="chip">{t('source.' + d.source) !== 'source.' + d.source ? t('source.' + d.source) : d.source}</span></td>
                <td className="small muted">{formatAgo(lang, d.createdAt)}</td>
                <td style={{ textAlign: 'right' }}>
                  <div className="row" style={{ justifyContent: 'flex-end', gap: 6 }}>
                    {status === 'discovered' ? (
                      <>
                        {integs.length > 0 && (
                          <select className="sm" aria-label={t('inbox.addAs')} value={kinds[d.id] ?? (d.kind !== 'generic' ? d.kind : '')} onChange={(e) => setKinds({ ...kinds, [d.id]: e.target.value })}>
                            <option value="">{t('inbox.asGeneric')}</option>
                            {integs.map((i) => <option key={i.id} value={i.id}>{i.name[lang] || i.name.en}</option>)}
                          </select>
                        )}
                        <button className="btn sm" type="button" onClick={() => setStatus(d, 'active')}>{t('inbox.add')}</button>
                        <button className="btn ghost sm" type="button" onClick={() => setStatus(d, 'ignored')}>{t('inbox.ignore')}</button>
                      </>
                    ) : (
                      <button className="btn ghost sm" type="button" onClick={() => setStatus(d, 'discovered')}>{t('ignored.restore')}</button>
                    )}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

type ScanFound = { ip: string; name: string; mac?: string; vendor?: string; deviceId?: number; deviceName?: string; status: 'new' | 'inbox' | 'known' | 'ignored' | 'error' };
type ScanResult = { ranges: string[]; scanned: number; alive: number; new: number; found: ScanFound[]; at: number };

function ScanModal({ onClose }: { onClose: () => void }) {
  const t = useT();
  const lang = useLang();
  const [ranges, setRanges] = useState<string[]>(['']);
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);
  const [result, setResult] = useState<ScanResult | null>(null);
  const [filed, setFiled] = useState<Record<number, 'active' | 'ignored'>>({});
  const [hideKnown, setHideKnown] = useState(false);
  const { busy, error, run } = useAction();
  const act = useAction();
  useEffect(() => {
    get<{ ranges: string[]; running: boolean; last: ScanResult | null }>('/api/discovery').then((i) => {
      setRanges(i.ranges?.length ? i.ranges : ['192.168.1.0/24']);
      if (i.running) setProgress({ done: 0, total: 0 });
      if (i.last) setResult(i.last);
    }).catch(() => {});
  }, []);
  useEvents({
    'discovery.progress': (p: { done: number; total: number }) => setProgress(p),
    'discovery.done': (r: ScanResult) => { setProgress(null); setResult(r); setFiled({}); },
  });
  const start = (e: FormEvent) => {
    e.preventDefault();
    setResult(null);
    run(async () => {
      const r = await post<{ hosts: number }>('/api/discovery/scan', { ranges: ranges.map((x) => x.trim()).filter(Boolean) });
      setProgress({ done: 0, total: r.hosts });
    });
  };
  const file = (f: ScanFound, s: 'active' | 'ignored') => act.run(async () => {
    await post(`/api/devices/${f.deviceId}/status`, { status: s });
    setFiled((c) => ({ ...c, [f.deviceId!]: s }));
  });
  const scanningNow = progress !== null;
  const rows = (result?.found ?? []).filter((f) => !hideKnown || f.status === 'new' || f.status === 'inbox');
  const statusChip = (f: ScanFound) => {
    const s = f.deviceId && filed[f.deviceId] ? (filed[f.deviceId] === 'active' ? 'known' : 'ignored') : f.status;
    const cls = s === 'new' ? 'pill ok' : s === 'inbox' ? 'pill warn' : s === 'known' ? 'pill accent' : 'pill off';
    return <span className={cls}>{t('discovery.status.' + s)}</span>;
  };
  return (
    <Modal wide title={t('discovery.title')} onClose={onClose} footer={
      <>
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.close')}</button>
        <button className="btn" type="submit" form="scan-form" disabled={busy || scanningNow}>{t('discovery.start')}</button>
      </>
    }>
      <form id="scan-form" className="stack" onSubmit={start}>
        <p className="small muted">{t('discovery.intro')}</p>
        <div className="field">
          {t('discovery.ranges')}
          {ranges.map((r, i) => (
            <div key={i} className="row" style={{ flexWrap: 'nowrap' }}>
              <input id={`scan-cidr-${i}`} type="text" className="mono grow" value={r} placeholder="192.168.90.0/24" aria-label={t('discovery.range')}
                onChange={(e) => setRanges((c) => c.map((x, j) => (j === i ? e.target.value : x)))} required={i === 0} />
              {ranges.length > 1 && <button className="btn ghost sm" type="button" aria-label={t('common.delete')} onClick={() => setRanges((c) => c.filter((_, j) => j !== i))}><IconTrash /></button>}
            </div>
          ))}
          <div><button className="btn ghost sm" type="button" onClick={() => setRanges((c) => [...c, ''])}><IconPlus />{t('discovery.addRange')}</button></div>
          <span className="hint">{t('discovery.rangesHint')}</span>
        </div>
      </form>
      {scanningNow && <div className="alert info">{progress!.total ? t('discovery.progress', { done: progress!.done, total: progress!.total }) : t('discovery.running')}</div>}
      <ErrorAlert error={error || act.error} />
      {result && (
        <>
          <div className="row">
            <span className="grow">{t('discovery.result', { alive: result.alive, scanned: result.scanned, new: result.new })}
              <span className="muted small"> · {formatAgo(lang, result.at)}</span></span>
            <label className="row small" style={{ gap: 6 }}><input type="checkbox" checked={hideKnown} onChange={(e) => setHideKnown(e.target.checked)} />{t('discovery.hideKnown')}</label>
          </div>
          {rows.length > 0 && (
            <div className="table-wrap">
              <table className="tbl">
                <thead><tr><th>{t('devices.colHost')}</th><th>{t('devices.colName')}</th><th>{t('devices.colMac')}</th><th>{t('devices.colState')}</th><th></th></tr></thead>
                <tbody>
                  {rows.map((f) => {
                    const pending = f.deviceId && !filed[f.deviceId] && (f.status === 'new' || f.status === 'inbox');
                    return (
                      <tr key={f.ip}>
                        <td className="mono small">{f.ip}</td>
                        <td>{f.deviceId && (f.status === 'known' || filed[f.deviceId] === 'active') ? <Link to={`/devices/${f.deviceId}`} onClick={onClose}>{f.deviceName || f.name}</Link> : (f.deviceName || f.name)}</td>
                        <td className="small">{f.mac ? <><span className="mono">{f.mac}</span>{f.vendor && <div className="muted">{f.vendor}</div>}</> : ''}</td>
                        <td>{statusChip(f)}</td>
                        <td style={{ textAlign: 'right' }}>
                          {pending && (
                            <div className="row" style={{ justifyContent: 'flex-end', gap: 6, flexWrap: 'nowrap' }}>
                              <button className="btn sm" type="button" disabled={act.busy} onClick={() => file(f, 'active')}>{t('inbox.add')}</button>
                              <button className="btn ghost sm" type="button" disabled={act.busy} onClick={() => file(f, 'ignored')}>{t('inbox.ignore')}</button>
                            </div>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </Modal>
  );
}
