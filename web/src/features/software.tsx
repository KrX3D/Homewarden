// Integrations (marketplace), device software (scan/update) and the Updates overview.
import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import {
  del, get, post, put, type CatalogEntry, type Component, type Credential, type Device, type Integration,
  type IntegrationSetting, type Job, type JobStatus, type Software, type Text,
} from '../api';
import { formatAgo, formatTime, useLang, useT, type Lang } from '../i18n';
import { useSession } from '../session';
import { useEvents } from '../useEvents';
import { ConfirmButton, ErrorAlert, Modal, Spinner, useAction } from '../components/ui';
import { IconJobs, IconPuzzle, IconRefresh, IconUpdates } from '../components/icons';
import { deviceSections, navItems } from '../extensions';
import { PortsList, type Port } from './network';
import { ActivityPage, JobPill } from './activity';
import { Howto, IntegrationSetupModal, SettingField, cleanSettings, credentialTypesOf, useIntegrationDefaults } from './integrationSetup';
import { CredentialModal } from './credentials';

export const tx = (text: Text | undefined, lang: Lang) => (text ? text[lang] || text.en || '' : '');

/** Compares dotted versions numerically (1.10.0 > 1.9.3). */
export function cmpVersion(a: string, b: string): number {
  const pa = a.split('.').map(Number), pb = b.split('.').map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] || 0) - (pb[i] || 0);
    if (d) return d > 0 ? 1 : -1;
  }
  return 0;
}

/** Integration icons come from catalogs, so they render as images, where scripts cannot run. */
export function IntegrationIcon({ svg, size = 28 }: { svg?: string; size?: number }) {
  if (!svg) return <span style={{ width: size, height: size, display: 'inline-grid', placeItems: 'center', color: 'var(--muted)' }}><IconPuzzle /></span>;
  const src = 'data:image/svg+xml;utf8,' + encodeURIComponent(svg.replace(/currentColor/g, '#5b7be0'));
  return <img src={src} width={size} height={size} alt="" style={{ flex: 'none' }} />;
}

export { JobPill };

/** Counts shown next to sidebar entries. */
export function useBadges(): Record<string, number> {
  const { can } = useSession();
  const [updates, setUpdates] = useState(0);
  const [integ, setInteg] = useState(0);
  const load = useCallback(() => {
    if (!can('devices.view')) return;
    get<{ components: Component[] }>('/api/updates').then((r) => setUpdates(r.components.length)).catch(() => {});
  }, [can]);
  // Integrations with a newer catalog version (server caches the catalog for 30 min).
  const loadInteg = useCallback(() => {
    if (!can('integrations.manage')) return;
    get<{ updates: unknown[] }>('/api/integrations/updates').then((r) => setInteg(r.updates.length)).catch(() => {});
  }, [can]);
  useEffect(() => { load(); loadInteg(); }, [load, loadInteg]);
  useEvents({ 'device.software': load, 'device.changed': load, 'integrations.changed': loadInteg, 'integrations.updates': loadInteg }, load);
  return { updates, integrations: integ };
}

// ---------------------------------------------------------------- updates page

function UpdatesPage() {
  const t = useT();
  const { can } = useSession();
  const integrations = useIntegrations();
  const [data, setData] = useState<{ components: Component[]; devices: Device[] } | null>(null);
  const [selected, setSelected] = useState<Record<number, Set<string>>>({});
  const [expanded, setExpanded] = useState<Record<number, boolean>>({});
  const [confirm, setConfirm] = useState<{ device: Device; comps: Component[]; all: boolean } | null>(null);
  const [started, setStarted] = useState<Record<number, Job>>({});
  const load = useCallback(() => { get<{ components: Component[]; devices: Device[] }>('/api/updates').then(setData).catch(() => {}); }, []);
  useEffect(() => { load(); }, [load]);
  useEvents({
    'device.software': load,
    'job.status': (e: { id: number; status: JobStatus }) => setStarted((cur) => {
      const hit = Object.entries(cur).find(([, j]) => j.id === e.id);
      return hit ? { ...cur, [hit[0]]: { ...hit[1], status: e.status } } : cur;
    }),
  }, load);

  if (!data) return <Spinner />;
  const byDevice = new Map<number, Component[]>();
  for (const c of data.components) byDevice.set(c.deviceId, [...(byDevice.get(c.deviceId) ?? []), c]);
  const devices = data.devices.filter((d) => byDevice.has(d.id));
  const security = data.components.filter((c) => c.security).length;
  const toggle = (d: number, id: string) => setSelected((cur) => {
    const n = new Set(cur[d] ?? []);
    if (n.has(id)) n.delete(id); else n.add(id);
    return { ...cur, [d]: n };
  });

  return (
    <>
      <div className="page-head">
        <h1 className="grow">{t('updates.title')}</h1>
        {data.components.length > 0 && <span className="muted">{t('updates.summary', { n: data.components.length, d: devices.length, s: security })}</span>}
      </div>
      {/* Started here: stays visible even when the device has no updates left afterwards. */}
      {Object.values(started).map((job) => (
        <div key={job.id} className={'alert row ' + (job.status === 'failed' ? 'error' : job.status === 'blocked' ? 'warn' : job.status === 'success' ? 'ok' : 'info')}>
          {(job.status === 'running' || job.status === 'queued') && <Spinner />}
          <span className="grow"><strong>{job.deviceName}</strong>: {t('upd.started.' + job.status)}</span>
          <Link to={`/activity/${job.id}`} className="small">{t('sw.showLog')}</Link>
          {job.status !== 'running' && job.status !== 'queued' && (
            <button className="btn subtle sm" type="button" aria-label={t('common.close')} onClick={() => setStarted((cur) => { const n = { ...cur }; delete n[job.deviceId!]; return n; })}>×</button>
          )}
        </div>
      ))}
      {devices.length === 0 ? (
        <div className="card empty"><IconUpdates /><h2>{t('updates.emptyTitle')}</h2><p>{t('updates.emptyText')}</p></div>
      ) : devices.map((d) => {
        const comps = byDevice.get(d.id)!;
        const updatable = comps.filter((c) => c.updatable && c.group !== 'action');
        const sel = selected[d.id] ?? new Set<string>();
        const shown = expanded[d.id] ? comps : comps.slice(0, 12);
        return (
          <section className="card" key={d.id}>
            <div className="card-head">
              <h2 className="grow"><Link to={`/devices/${d.id}`} style={{ color: 'inherit', textDecoration: 'none' }}>{d.name}</Link></h2>
              <span className="muted small">{t('updates.count', { n: comps.length })}</span>
            </div>
            <ComponentTable comps={shown} selected={sel} onToggle={can('updates.run') ? (id) => toggle(d.id, id) : undefined} />
            {comps.length > 12 && (
              <button className="btn subtle sm" type="button" onClick={() => setExpanded((c) => ({ ...c, [d.id]: !c[d.id] }))}>
                {expanded[d.id] ? t('updates.less') : t('updates.more', { n: comps.length - 12 })}
              </button>
            )}
            {can('updates.run') && updatable.length > 0 && (
              <div className="row">
                <button className="btn" type="button" disabled={sel.size === 0} onClick={() => setConfirm({ device: d, comps: updatable.filter((c) => sel.has(c.id)), all: false })}>{t('sw.updateSelected', { n: sel.size })}</button>
                <button className="btn ghost" type="button" onClick={() => setConfirm({ device: d, comps: updatable, all: true })}>{t('updates.updateAll')}</button>
              </div>
            )}
          </section>
        );
      })}
      {confirm && <UpdateModal device={confirm.device} comps={confirm.comps} all={confirm.all}
        busyCheck={!!integrations.find((i) => i.id === confirm.device.kind)?.capabilities.includes('busy')}
        onClose={() => setConfirm(null)} onStarted={(job) => {
          setStarted((cur) => ({ ...cur, [confirm.device.id]: job }));
          setSelected((cur) => ({ ...cur, [confirm.device.id]: new Set() }));
          setConfirm(null);
        }} />}
    </>
  );
}

function WarningPill({ c }: { c: Component }) {
  const t = useT();
  const w = c.meta?.warning as string | undefined;
  if (!w) return null;
  return <span className="pill warn" title={t('warnHint.' + w)}>{t('warn.' + w)}</span>;
}

/**
 * Confirms an update: what will be updated, what the warnings mean, and the busy
 * override. Starting it returns the job, so the caller can link to its log.
 */
export function UpdateModal({ device, comps, all, busyCheck, onClose, onStarted }: {
  device: { id: number; name: string }; comps: Component[]; all: boolean; busyCheck: boolean;
  onClose: () => void; onStarted: (job: Job) => void;
}) {
  const t = useT();
  const [force, setForce] = useState(false);
  const { busy, error, run } = useAction();
  const warnings = [...new Set(comps.map((c) => c.meta?.warning as string | undefined).filter((w): w is string => !!w))];
  const start = () => run(async () => {
    const job = await post<Job>(`/api/devices/${device.id}/update`, { components: all ? ['*'] : comps.map((c) => c.id), force });
    onStarted(job);
  });
  return (
    <Modal wide title={t('upd.title', { name: device.name })} onClose={onClose} footer={
      <>
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button id="upd-start" className="btn" type="button" disabled={busy || comps.length === 0} onClick={start}>{t('upd.start', { n: comps.length })}</button>
      </>
    }>
      <p className="small muted" style={{ margin: 0 }}>{all ? t('upd.introAll', { n: comps.length }) : t('upd.intro', { n: comps.length })}</p>
      <div className="table-wrap" style={{ maxHeight: '40vh', overflow: 'auto' }}>
        <table className="tbl"><tbody>
          {comps.map((c) => (
            <tr key={c.id}>
              <td><strong>{c.name}</strong>{c.meta?.summary && <span className="muted small"> · {c.meta.summary}</span>}</td>
              <td className="mono small">{c.installed || '–'} → {c.available || c.meta?.summary || t('sw.yes')}</td>
              <td style={{ textAlign: 'right' }}><span className="row" style={{ gap: 6, justifyContent: 'flex-end' }}>
                {c.security && <span className="pill crit">{t('sw.security')}</span>}<WarningPill c={c} />
              </span></td>
            </tr>
          ))}
        </tbody></table>
      </div>
      {warnings.length > 0 && (
        <div className="stack" style={{ gap: 6 }}>
          <strong className="small">{t('upd.whatHappens')}</strong>
          <ul className="plain small">
            {warnings.map((w) => <li key={w}><span className="pill warn">{t('warn.' + w)}</span> {t('warnHint.' + w)}</li>)}
          </ul>
        </div>
      )}
      {busyCheck && <label className="check small"><input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />{t('sw.force')}</label>}
      <ErrorAlert error={error} />
    </Modal>
  );
}

/** The outcome of the newest update job, when it did not succeed. */
export function LastUpdateProblem({ jobs }: { jobs: Job[] }) {
  const t = useT();
  const last = [...jobs].filter((j) => j.type === 'update').sort((a, b) => b.id - a.id)[0];
  if (!last || (last.status !== 'failed' && last.status !== 'blocked')) return null;
  const r = last.result as { error?: string; reason?: string } | null;
  const why = (r && (r.error || r.reason)) || '';
  return (
    <div className={'alert ' + (last.status === 'blocked' ? 'warn' : 'error')}>
      <strong>{last.status === 'blocked' ? t('upd.lastBlocked') : t('upd.lastFailed')}</strong>{why && <>: {why}</>}{' '}
      <Link to={`/activity/${last.id}`} className="small">{t('sw.showLog')}</Link>
    </div>
  );
}

function ComponentTable({ comps, selected, onToggle, onChangelog, onChoose }: {
  comps: Component[]; selected?: Set<string>; onToggle?: (id: string) => void;
  onChangelog?: (c: Component) => void; onChoose?: (c: Component) => void;
}) {
  const t = useT();
  return (
    <div className="table-wrap">
      <table className="tbl">
        <thead><tr>{onToggle && <th style={{ width: 32 }}></th>}<th>{t('sw.component')}</th><th>{t('sw.installed')}</th><th>{t('sw.available')}</th><th></th></tr></thead>
        <tbody>
          {comps.map((c) => (
            <tr key={c.id}>
              {onToggle && <td>{c.updateAvailable && c.updatable && <input type="checkbox" aria-label={c.name} checked={selected!.has(c.id)} onChange={() => onToggle(c.id)} />}</td>}
              <td><strong>{c.name}</strong>{c.meta?.summary && <span className="muted small"> · {c.meta.summary}</span>} <span className="chip">{t('group.' + c.group) !== 'group.' + c.group ? t('group.' + c.group) : c.group}</span></td>
              <td className="mono small">{c.installed || '–'}</td>
              <td className="mono small">{c.updateAvailable ? (c.available || t('sw.yes')) : <span className="muted">{t('sw.current')}</span>}</td>
              <td style={{ textAlign: 'right' }}>
                <span className="row" style={{ gap: 6, justifyContent: 'flex-end' }}>
                  {c.security && c.updateAvailable && <span className="pill crit">{t('sw.security')}</span>}
                  {!c.updatable && c.updateAvailable && <span className="pill off" title={t('sw.notifyOnlyHint')}>{t('sw.notifyOnly')}</span>}
                  <WarningPill c={c} />
                  {onChangelog && (c.updateAvailable || c.group === 'container') && c.group !== 'action' ? <button type="button" className="btn subtle sm" onClick={() => onChangelog(c)}>{t('sw.changelog')}</button>
                    : c.meta?.changelog && <a className="small" href={c.meta.changelog} target="_blank" rel="noreferrer">{t('sw.changelog')}</a>}
                  {onChoose && c.group === 'container' && c.updatable && <button type="button" className="btn subtle sm" onClick={() => onChoose(c)}>{t('sw.choose')}</button>}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ---------------------------------------------------------------- device cards

function useIntegrations() {
  const [list, setList] = useState<Integration[]>([]);
  const load = useCallback(() => { get<Integration[]>('/api/integrations').then(setList).catch(() => {}); }, []);
  useEffect(() => { load(); }, [load]);
  useEvents({ 'integrations.changed': load });
  return list;
}

function IntegrationCard({ device }: { device: Device }) {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const list = useIntegrations();
  const [creds, setCreds] = useState<Credential[]>([]);
  const savedSettings = ((device.config as { settings?: Record<string, unknown> }).settings ?? {});
  const [kind, setKind] = useState(device.kind);
  const [values, setValues] = useState<Record<string, unknown>>(savedSettings);
  const [howto, setHowto] = useState(false);
  const [saved, setSaved] = useState(false);
  const [setup, setSetup] = useState(false);
  const [newCredFor, setNewCredFor] = useState<IntegrationSetting | null>(null);
  const { busy, error, run } = useAction();
  const manage = can('devices.manage');
  const def = useIntegrationDefaults(kind, manage && !setup);
  useEffect(() => { setKind(device.kind); setValues(savedSettings); }, [device.kind, JSON.stringify(savedSettings)]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (manage) get<Credential[]>('/api/credentials').then(setCreds).catch(() => {}); }, [manage]);

  const integ = list.find((i) => i.id === kind);
  if (device.parentId && device.kind === 'generic' && !manage) return null;

  // Empty fields are not saved: the device then follows the integration setup, and
  // after that the manifest default.
  const save = (e: FormEvent) => {
    e.preventDefault();
    setSaved(false);
    run(async () => {
      await put(`/api/devices/${device.id}/integration`, { kind, settings: cleanSettings(values) });
      setSaved(true);
    });
  };
  const applyPresence = () => run(async () => {
    await put(`/api/devices/${device.id}`, { ...device, presence: integ!.presence });
  });
  const inherited = (s: IntegrationSetting) => def?.settings?.[s.key] ?? s.default;
  const sharedSettings = integ?.settings?.filter((s) => s.scope !== 'device') ?? [];
  const ownShared = sharedSettings.filter((s) => values[s.key] !== undefined && values[s.key] !== null && values[s.key] !== '').length;

  return (
    <section className="card">
      <div className="card-head">
        {integ && <IntegrationIcon svg={integ.iconSvg} />}
        <h2 className="grow">{t('integ.card')}</h2>
        {integ && <span className="chip">{integ.version}</span>}
      </div>
      {list.length === 0 ? (
        <p className="small muted">{t('integ.noneInstalled')} {can('integrations.manage') && <Link to="/integrations">{t('integ.openMarket')}</Link>}</p>
      ) : manage ? (
        <form className="stack" onSubmit={save}>
          <label className="field">
            {t('integ.type')}
            <select id="dev-integration" value={kind} onChange={(e) => { setKind(e.target.value); setValues({}); }}>
              <option value="generic">{t('integ.generic')}</option>
              {list.map((i) => <option key={i.id} value={i.id}>{tx(i.name, lang)}</option>)}
            </select>
            {integ && <span className="hint">{tx(integ.description, lang)}</span>}
          </label>
          {integ && (
            <div className="alert info small row" style={{ gap: 8 }}>
              <span className="grow">{t('integ.sharedHint', { name: tx(integ.name, lang) })}</span>
              {can('integrations.manage') && <button className="btn ghost sm" type="button" onClick={() => setSetup(true)}>{t('integSetup.open')}</button>}
            </div>
          )}
          {integ?.settings?.filter((s) => s.scope === 'device').map((s) => <SettingField key={s.key} s={s} value={values[s.key]} creds={creds} inherited={s.default} onChange={(v) => setValues({ ...values, [s.key]: v })} />)}
          {sharedSettings.length > 0 && (
            <details open={ownShared > 0} className="howto-box">
              <summary>{ownShared > 0 ? t('integ.overridesSet', { n: ownShared }) : t('integ.overrides')}</summary>
              <div className="stack" style={{ marginTop: 10 }}>
                <p className="small muted" style={{ margin: 0 }}>{t('integ.overridesHint')}</p>
                {sharedSettings.map((s) => <SettingField key={s.key} s={s} value={values[s.key]} creds={creds} inherited={inherited(s)} onChange={(v) => setValues({ ...values, [s.key]: v })}
                  onNewCredential={can('credentials.manage') ? () => setNewCredFor(s) : undefined} />)}
              </div>
            </details>
          )}
          <ErrorAlert error={error} />
          {saved && <div className="alert ok">{t('conn.saved')}</div>}
          <div className="row">
            <button className="btn" type="submit" disabled={busy}>{t('common.save')}</button>
            {integ?.howto && <button className="btn subtle" type="button" onClick={() => setHowto((h) => !h)}>{howto ? t('integ.hideHowto') : t('integ.showHowto')}</button>}
            {integ?.presence && JSON.stringify(integ.presence) !== JSON.stringify(device.presence) && (
              <button className="btn subtle" type="button" onClick={applyPresence} title={t('integ.presenceHint')}>{t('integ.usePresence')}</button>
            )}
          </div>
        </form>
      ) : (
        <p>{integ ? tx(integ.name, lang) : t('integ.generic')}</p>
      )}
      {howto && integ?.howto && <Howto text={tx(integ.howto, lang)} />}
      {integ && (integ as Integration & { ports?: Port[] }).ports?.length ? (
        <details>
          <summary className="small" style={{ cursor: 'pointer' }}>{t('net.needs')}</summary>
          <div style={{ marginTop: 8 }}><PortsList ports={(integ as Integration & { ports?: Port[] }).ports} /></div>
        </details>
      ) : null}
      {setup && integ && <IntegrationSetupModal integ={integ} onClose={() => setSetup(false)} />}
      {newCredFor && integ && <CredentialModal cred={null} types={credentialTypesOf(newCredFor)} fromSetting
        tokenUserLabel={tx(newCredFor.credentialUsername, lang) || undefined} defaultName={`${tx(integ.name, lang)} ${tx(newCredFor.label, lang)}`}
        onClose={() => setNewCredFor(null)} onSaved={(c) => {
          setCreds((cur) => [...cur.filter((x) => x.id !== c.id), c]);
          setValues((cur) => ({ ...cur, [newCredFor.key]: c.id }));
          setNewCredFor(null);
        }} />}
    </section>
  );
}

function SoftwareCard({ device }: { device: Device }) {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const integrations = useIntegrations();
  const integ = integrations.find((i) => i.id === device.kind);
  const [sw, setSw] = useState<Software | null>(null);
  const [onlyUpdates, setOnlyUpdates] = useState(true);
  const [filter, setFilter] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [confirm, setConfirm] = useState<{ comps: Component[]; all: boolean } | null>(null);
  const [logFor, setLogFor] = useState<Component | null>(null);
  const [chooseFor, setChooseFor] = useState<Component | null>(null);
  const { busy, error, run } = useAction();
  const load = useCallback(() => { get<Software>(`/api/devices/${device.id}/software`).then(setSw).catch(() => {}); }, [device.id]);
  useEffect(() => { load(); }, [load]);
  useEvents({
    'device.software': (e: { id: number }) => e.id === device.id && load(),
    'job.status': (e: { deviceId: number | null }) => e.deviceId === device.id && load(),
  }, load);

  const comps = useMemo(() => {
    let c = sw?.components ?? [];
    if (onlyUpdates) c = c.filter((x) => x.updateAvailable || x.group === 'os');
    const q = filter.trim().toLowerCase();
    if (q) c = c.filter((x) => x.name.toLowerCase().includes(q));
    return c;
  }, [sw, onlyUpdates, filter]);

  if (device.kind === 'generic' || !integ) return null;
  if (!sw) return <section className="card"><Spinner /></section>;

  const running = sw.jobs.find((j) => j.status === 'running' || j.status === 'queued');
  const pending = sw.components.filter((c) => c.updateAvailable && c.updatable);
  const facts = sw.facts.facts ?? {};
  const toggle = (id: string) => setSelected((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const scan = () => run(async () => { await post(`/api/devices/${device.id}/scan`); load(); });

  return (
    <section className="card">
      <div className="card-head">
        <h2 className="grow">{t('sw.title')}</h2>
        {sw.facts.scannedAt > 0 && <span className="small muted">{t('sw.scanned', { ago: formatAgo(lang, sw.facts.scannedAt) })}</span>}
        <button className="btn ghost sm" type="button" onClick={scan} disabled={busy || !!running}><IconRefresh />{t('sw.scanNow')}</button>
      </div>
      {running && <div className="alert info row"><Spinner /><span>{running.title}</span><Link to={`/activity/${running.id}`} className="small">{t('sw.showLog')}</Link></div>}
      {sw.facts.error && <div className="alert error">{t('sw.scanFailed')}: {sw.facts.error}</div>}
      {!running && <LastUpdateProblem jobs={sw.jobs} />}
      {facts.eolPassed && <div className="alert warn">{t('sw.eol', { date: facts.eol })}</div>}
      {facts.rebootRequired && <div className="alert warn">{t('sw.rebootRequired')}</div>}
      {Array.isArray(facts.warnings) && facts.warnings.map((w: string) => <div key={w} className="alert warn">{t('factWarn.' + w) !== 'factWarn.' + w ? t('factWarn.' + w) : w}{facts.warningDetails?.[w] && <div className="small">{facts.warningDetails[w]}</div>}</div>)}
      {sw.facts.scannedAt === 0 && !running && !sw.facts.error && <p className="muted small">{t('sw.neverScanned')}</p>}
      <ErrorAlert error={error} />

      {sw.components.length > 0 && (
        <>
          <div className="row">
            <label className="check"><input type="checkbox" checked={onlyUpdates} onChange={(e) => setOnlyUpdates(e.target.checked)} />{t('sw.onlyUpdates', { n: pending.length })}</label>
            <span style={{ flex: 1 }} />
            {sw.components.length > 10 && <input id="sw-filter" type="text" placeholder={t('sw.filter')} value={filter} onChange={(e) => setFilter(e.target.value)} style={{ maxWidth: 220 }} />}
          </div>
          <ComponentTable comps={comps} selected={selected} onToggle={can('updates.run') ? toggle : undefined}
            onChangelog={integ.capabilities.includes('changelog') ? setLogFor : undefined}
            onChoose={integ.capabilities.includes('choose') && can('updates.run') ? setChooseFor : undefined} />
          {can('updates.run') && pending.length > 0 && (
            <div className="row">
              <button className="btn" type="button" disabled={busy || selected.size === 0 || !!running} onClick={() => setConfirm({ comps: pending.filter((c) => selected.has(c.id)), all: false })}>{t('sw.updateSelected', { n: selected.size })}</button>
              <button className="btn ghost" type="button" disabled={busy || !!running} onClick={() => setConfirm({ comps: pending, all: true })}>{t('sw.updateAll')}</button>
            </div>
          )}
        </>
      )}

      {Object.keys(facts).length > 0 && (
        <details>
          <summary className="small" style={{ cursor: 'pointer' }}>{t('sw.facts')}</summary>
          <dl className="kv" style={{ marginTop: 8 }}>
            {Object.entries(facts).filter(([, v]) => v !== '' && v !== null && typeof v !== 'object').map(([k, v]) => (
              [<dt key={k + 'k'}>{k}</dt>, <dd key={k + 'v'} className="mono small">{String(v)}</dd>]
            ))}
          </dl>
        </details>
      )}
      {sw.history.length > 0 && (
        <details>
          <summary className="small" style={{ cursor: 'pointer' }}>{t('sw.history', { n: sw.history.length })}</summary>
          <div className="table-wrap" style={{ marginTop: 8 }}>
            <table className="tbl"><tbody>
              {sw.history.map((h, i) => <tr key={i}><td>{h.name}</td><td className="mono small">{h.oldVersion} → {h.newVersion}</td><td className="small muted">{formatTime(lang, h.at)}</td></tr>)}
            </tbody></table>
          </div>
        </details>
      )}
      {sw.jobs.length > 0 && (
        <details>
          <summary className="small" style={{ cursor: 'pointer' }}>{t('sw.recentJobs')}</summary>
          <JobList jobs={sw.jobs} compact />
        </details>
      )}
      {confirm && <UpdateModal device={device} comps={confirm.comps} all={confirm.all} busyCheck={integ.capabilities.includes('busy')}
        onClose={() => setConfirm(null)} onStarted={() => { setConfirm(null); setSelected(new Set()); load(); }} />}
      {logFor && <ChangelogModal device={device} comp={logFor} onClose={() => setLogFor(null)} />}
      {chooseFor && <ChooseModal device={device} comp={chooseFor} onClose={() => setChooseFor(null)} onDone={() => { setChooseFor(null); load(); }} />}
    </section>
  );
}

function ChangelogModal({ device, comp, onClose }: { device: Device; comp: Component; onClose: () => void }) {
  const t = useT();
  const [data, setData] = useState<{ text: string; url: string } | null>(null);
  const { error, run } = useAction();
  useEffect(() => { run(async () => setData(await get(`/api/devices/${device.id}/changelog?component=${encodeURIComponent(comp.id)}`))); }, [comp.id]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Modal wide title={t('sw.changelogOf', { name: comp.name })} onClose={onClose} footer={<button className="btn" type="button" onClick={onClose}>{t('common.close')}</button>}>
      {!data && !error && <Spinner />}
      {data && (data.text ? <pre className="log" style={{ maxHeight: '60vh' }}>{data.text}</pre> : <p className="muted small">{t('sw.noChangelog')}</p>)}
      {data?.url && <a className="small" href={data.url} target="_blank" rel="noreferrer">{data.url}</a>}
      <ErrorAlert error={error} />
    </Modal>
  );
}

function ChooseModal({ device, comp, onClose, onDone }: { device: Device; comp: Component; onClose: () => void; onDone: () => void }) {
  const t = useT();
  const [data, setData] = useState<{ current: string; choices: { value: string; label: string; note?: string }[] } | null>(null);
  const [pick, setPick] = useState('');
  const { busy, error, run } = useAction();
  useEffect(() => { run(async () => setData(await get(`/api/devices/${device.id}/choices?component=${encodeURIComponent(comp.id)}`))); }, [comp.id]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Modal title={t('sw.chooseFor', { name: comp.name })} onClose={onClose} footer={
      <>
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button className="btn" type="button" disabled={busy || !pick} onClick={() => run(async () => { await post(`/api/devices/${device.id}/update`, { components: [comp.id + '@' + pick] }); onDone(); })}>{t('sw.switch')}</button>
      </>
    }>
      {!data && !error && <Spinner />}
      {data && (
        <div className="stack">
          <p className="small">{t('sw.chooseIntro', { current: data.current })}</p>
          <select id="choose-version" value={pick} onChange={(e) => setPick(e.target.value)} size={Math.min(12, Math.max(4, data.choices.length))}>
            {data.choices.map((c) => <option key={c.value} value={c.value}>{c.label}{c.note ? ` (${c.note === 'on this host' ? t('sw.onHost') : c.note})` : ''}</option>)}
          </select>
          <p className="small muted">{t('sw.chooseHint')}</p>
        </div>
      )}
      <ErrorAlert error={error} />
    </Modal>
  );
}

// ---------------------------------------------------------------- jobs

function JobList({ jobs, compact }: { jobs: Job[]; compact?: boolean }) {
  const t = useT();
  const lang = useLang();
  const nav = useNavigate();
  return (
    <div className="table-wrap" style={{ marginTop: compact ? 8 : 0 }}>
      <table className="tbl">
        <thead><tr><th>{t('jobs.what')}</th>{!compact && <th>{t('jobs.by')}</th>}<th>{t('jobs.when')}</th><th>{t('jobs.status')}</th></tr></thead>
        <tbody>
          {jobs.map((j) => (
            <tr key={j.id} className="clickable" onClick={() => nav(`/activity/${j.id}`)}>
              <td>{j.title}</td>
              {!compact && <td className="small muted">{j.createdBy}</td>}
              <td className="small muted">{formatAgo(lang, j.createdAt)}</td>
              <td><JobPill status={j.status} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ---------------------------------------------------------------- integrations page

function IntegrationsPage() {
  const t = useT();
  const lang = useLang();
  const installed = useIntegrations();
  const [catalog, setCatalog] = useState<{ entries: CatalogEntry[]; errors: { source: string; error: string }[]; checkedAt: number } | null>(null);
  const [checking, setChecking] = useState(false);
  const [sources, setSources] = useState<string[]>([]);
  const [review, setReview] = useState<CatalogEntry | null>(null);
  const [setupId, setSetupId] = useState<string | null>(null);
  const setupInteg = installed.find((i) => i.id === setupId);
  const { error, run } = useAction();
  // The server keeps the last check (at start, every 6 h); "Check now" asks again.
  const loadCatalog = useCallback((refresh = false) => {
    if (refresh) setChecking(true);
    get<typeof catalog>('/api/integrations/catalog' + (refresh ? '?refresh=1' : '')).then(setCatalog).catch(() => {}).finally(() => setChecking(false));
    get<{ sources: string[] }>('/api/settings/catalog-sources').then((r) => setSources(r.sources)).catch(() => {});
  }, []);
  useEffect(() => { loadCatalog(); }, [loadCatalog]);
  useEvents({ 'integrations.updates': () => loadCatalog() });

  const byId = new Map<string, CatalogEntry[]>();
  for (const e of catalog?.entries ?? []) byId.set(e.id, [...(byId.get(e.id) ?? []), e]);
  const srcLabel = (s: string) => (s === 'builtin' ? t('market.builtin') : s.replace(/^https?:\/\//, '').split('/')[0]);
  // One offer per integration: the newest version wins; on a tie the built-in copy,
  // since it needs no network.
  const bestOf = (offers: CatalogEntry[]) => [...offers].sort((a, b) => cmpVersion(b.version, a.version) || (a.source === 'builtin' ? -1 : b.source === 'builtin' ? 1 : 0))[0];
  const available = [...byId.entries()].filter(([id]) => !installed.some((i) => i.id === id));
  const newerOf = (i: Integration) => {
    const offers = byId.get(i.id);
    const best = offers && bestOf(offers);
    return best && cmpVersion(best.version, i.version) > 0 ? best : null;
  };

  return (
    <>
      <div className="page-head">
        <h1 className="grow">{t('market.title')}</h1>
        {catalog && catalog.checkedAt > 0 && <span className="small muted">{t('market.checked', { ago: formatAgo(lang, catalog.checkedAt) })}</span>}
        <button className="btn ghost sm" type="button" disabled={checking} onClick={() => loadCatalog(true)}><IconRefresh />{checking ? t('market.checking') : t('market.checkNow')}</button>
      </div>
      <p className="muted">{t('market.intro')}</p>
      <ErrorAlert error={error} />

      {installed.length > 0 && (
        <section className="stack">
          <h2>{t('market.installed')}</h2>
          <div className="grid-2">
            {installed.map((i) => (
              <div className="card" key={i.id}>
                <div className="card-head">
                  <IntegrationIcon svg={i.iconSvg} />
                  <h3 className="grow">{tx(i.name, lang)}</h3>
                  <span className="chip">{i.version}</span>
                  {newerOf(i) && <span className="pill warn" title={srcLabel(newerOf(i)!.source)}>{t('market.newVersion', { v: newerOf(i)!.version })}</span>}
                </div>
                <p className="small muted">{tx(i.description, lang)}</p>
                <div className="row small muted">{t('market.from', { source: srcLabel(i.source) })}
                  <span style={{ flex: 1 }} />
                  {newerOf(i) && <button className="btn sm" type="button" onClick={() => setReview(newerOf(i)!)}>{t('market.updateTo', { v: newerOf(i)!.version })}</button>}
                  <button className="btn sm" type="button" onClick={() => setSetupId(i.id)}>{t('integSetup.open')}</button>
                  <ConfirmButton label={t('market.uninstall')} onConfirm={() => run(async () => { await del(`/api/integrations/${i.id}`); })} />
                </div>
              </div>
            ))}
          </div>
        </section>
      )}

      <section className="stack">
        <h2>{t('market.available')}</h2>
        {!catalog ? <Spinner /> : (
          <div className="grid-2">
            {available.map(([id, offers]) => {
              const first = offers[0];
              return (
                <div className="card" key={id}>
                  <div className="card-head">
                    <IntegrationIcon svg={first.iconSvg} />
                    <h3 className="grow">{tx(first.name, lang)}</h3>
                  </div>
                  <p className="small muted">{tx(first.description, lang)}</p>
                  {(() => {
                    const best = bestOf(offers);
                    const also = offers.filter((o) => o !== best && o.version === best.version).map((o) => srcLabel(o.source));
                    return (
                      <div className="row small">
                        <span className="chip">{best.version}</span>
                        <span className="muted">{srcLabel(best.source)}{also.length > 0 && ' · ' + t('market.alsoIn', { list: also.join(', ') })}</span>
                        <span style={{ flex: 1 }} />
                        <button className="btn sm" type="button" onClick={() => setReview(best)}>{t('market.install')}</button>
                      </div>
                    );
                  })()}
                </div>
              );
            })}
          </div>
        )}
        {catalog && available.length === 0 && <p className="small muted">{t('market.allInstalled')}</p>}
        {catalog?.errors.map((e) => <div key={e.source} className="alert warn small">{t('market.sourceError', { source: e.source })} <span className="mono">{e.error}</span></div>)}
      </section>

      <SourcesCard sources={sources} onSaved={(s) => { setSources(s); loadCatalog(true); }} />
      {review && <ReviewModal entry={review} onClose={() => setReview(null)} onInstalled={() => { if (!installed.some((i) => i.id === review.id)) setSetupId(review.id); setReview(null); loadCatalog(); }} />}
      {setupInteg && <IntegrationSetupModal integ={setupInteg} onClose={() => setSetupId(null)} />}
    </>
  );
}

function SourcesCard({ sources, onSaved }: { sources: string[]; onSaved: (s: string[]) => void }) {
  const t = useT();
  const [text, setText] = useState(sources.join('\n'));
  const { busy, error, run } = useAction();
  useEffect(() => setText(sources.join('\n')), [sources]);
  return (
    <section className="card">
      <h2>{t('market.sources')}</h2>
      <p className="small muted">{t('market.sourcesIntro')}</p>
      <textarea id="catalog-sources" rows={3} value={text} onChange={(e) => setText(e.target.value)} />
      <ErrorAlert error={error} />
      <div><button className="btn ghost" type="button" disabled={busy} onClick={() => run(async () => {
        const r = await put<{ sources: string[] }>('/api/settings/catalog-sources', { sources: text.split('\n') });
        onSaved(r.sources);
      })}>{t('common.save')}</button></div>
    </section>
  );
}

/** Line-level diff: marks lines that exist only on one side. Enough to review a script. */
function diffLines(a: string, b: string) {
  const A = a.split('\n'), B = b.split('\n');
  const n = A.length, m = B.length;
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = A[i] === B[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  const out: { t: ' ' | '+' | '-'; s: string }[] = [];
  let i = 0, j = 0;
  while (i < n && j < m) {
    if (A[i] === B[j]) { out.push({ t: ' ', s: A[i] }); i++; j++; }
    else if (dp[i + 1][j] >= dp[i][j + 1]) out.push({ t: '-', s: A[i++] });
    else out.push({ t: '+', s: B[j++] });
  }
  while (i < n) out.push({ t: '-', s: A[i++] });
  while (j < m) out.push({ t: '+', s: B[j++] });
  return out;
}

function ReviewModal({ entry, onClose, onInstalled }: { entry: CatalogEntry; onClose: () => void; onInstalled: () => void }) {
  const t = useT();
  const lang = useLang();
  const [data, setData] = useState<{ candidate: { version: string; script: string; permissions: Integration['permissions'] }; current?: { version: string; script: string } } | null>(null);
  const { busy, error, run } = useAction();
  useEffect(() => { run(async () => setData(await get(`/api/integrations/review?source=${encodeURIComponent(entry.source)}&id=${entry.id}`))); }, [entry]); // eslint-disable-line react-hooks/exhaustive-deps
  const p = entry.permissions;
  const perms = [p.ssh && t('perm2.ssh'), p.docker && t('perm2.docker'), p.httpDevice && t('perm2.httpDevice'), p.httpHosts?.length ? t('perm2.httpHosts', { hosts: p.httpHosts.join(', ') }) : ''].filter(Boolean) as string[];
  const lines = data ? (data.current ? diffLines(data.current.script, data.candidate.script) : data.candidate.script.split('\n').map((s) => ({ t: ' ' as const, s }))) : [];
  const changed = lines.filter((l) => l.t !== ' ').length;
  return (
    <Modal wide title={t('market.reviewTitle', { name: tx(entry.name, lang), v: entry.version })} onClose={onClose} footer={
      <>
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button className="btn" type="button" disabled={busy || !data} onClick={() => run(async () => { await post('/api/integrations/install', { source: entry.source, id: entry.id }); onInstalled(); })}>{data?.current ? t('market.update') : t('market.install')}</button>
      </>
    }>
      <div className="stack" style={{ gap: 6 }}>
        <strong className="small">{t('market.permissions')}</strong>
        <ul className="small" style={{ margin: 0, paddingLeft: 18 }}>{perms.length ? perms.map((x) => <li key={x}>{x}</li>) : <li>{t('perm2.none')}</li>}</ul>
      </div>
      {data?.current && <p className="small muted">{t('market.diff', { from: data.current.version, to: data.candidate.version, n: changed })}</p>}
      {data && (
        <details open={!!data.current}>
          <summary className="small" style={{ cursor: 'pointer' }}>{t('market.script')}</summary>
          <pre className="log diff">{lines.map((l, i) => <span key={i} className={l.t === '+' ? 'add' : l.t === '-' ? 'del' : undefined}>{l.t} {l.s}{'\n'}</span>)}</pre>
        </details>
      )}
      <ErrorAlert error={error} />
    </Modal>
  );
}

navItems.push(
  { path: 'updates', labelKey: 'nav.updates', perm: 'devices.view', icon: <IconUpdates />, element: <UpdatesPage /> },
  { path: 'activity', labelKey: 'nav.activity', perm: 'jobs.view', icon: <IconJobs />, element: <ActivityPage /> },
  { path: 'integrations', labelKey: 'nav.integrations', perm: 'integrations.manage', icon: <IconPuzzle />, element: <IntegrationsPage /> },
);
// Software first, then which integration, then how to connect.
deviceSections.splice(0, 0, SoftwareCard, IntegrationCard);
