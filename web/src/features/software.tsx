// Integrations (marketplace), device software (scan/update), the Updates overview and
// the job log.
import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react';
import { Link, Route, Routes, useNavigate, useParams } from 'react-router-dom';
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

const jobClass: Record<JobStatus, string> = { queued: 'off', running: 'accent', success: 'ok', failed: 'crit', blocked: 'warn', cancelled: 'off' };
export function JobPill({ status }: { status: JobStatus }) {
  const t = useT();
  return <span className={'pill dot ' + jobClass[status]}>{t('job.' + status)}</span>;
}

/** Counts shown next to sidebar entries. */
export function useBadges(): Record<string, number> {
  const { can } = useSession();
  const [updates, setUpdates] = useState(0);
  const load = useCallback(() => {
    if (!can('devices.view')) return;
    get<{ components: Component[] }>('/api/updates').then((r) => setUpdates(r.components.length)).catch(() => {});
  }, [can]);
  useEffect(() => { load(); }, [load]);
  useEvents({ 'device.software': load, 'device.changed': load }, load);
  return { updates };
}

// ---------------------------------------------------------------- updates page

function UpdatesPage() {
  const t = useT();
  const { can } = useSession();
  const [data, setData] = useState<{ components: Component[]; devices: Device[] } | null>(null);
  const { error, run } = useAction();
  const load = useCallback(() => { get<{ components: Component[]; devices: Device[] }>('/api/updates').then(setData).catch(() => {}); }, []);
  useEffect(() => { load(); }, [load]);
  useEvents({ 'device.software': load }, load);

  if (!data) return <Spinner />;
  const byDevice = new Map<number, Component[]>();
  for (const c of data.components) byDevice.set(c.deviceId, [...(byDevice.get(c.deviceId) ?? []), c]);
  const devices = data.devices.filter((d) => byDevice.has(d.id));
  const security = data.components.filter((c) => c.security).length;

  const start = (d: Device, ids: string[]) => run(async () => { await post(`/api/devices/${d.id}/update`, { components: ids }); });

  return (
    <>
      <div className="page-head">
        <h1 className="grow">{t('updates.title')}</h1>
        {data.components.length > 0 && <span className="muted">{t('updates.summary', { n: data.components.length, d: devices.length, s: security })}</span>}
      </div>
      <ErrorAlert error={error} />
      {devices.length === 0 ? (
        <div className="card empty"><IconUpdates /><h2>{t('updates.emptyTitle')}</h2><p>{t('updates.emptyText')}</p></div>
      ) : devices.map((d) => {
        const comps = byDevice.get(d.id)!;
        const updatable = comps.filter((c) => c.updatable && c.group !== 'action');
        return (
          <section className="card" key={d.id}>
            <div className="card-head">
              <h2 className="grow"><Link to={`/devices/${d.id}`} style={{ color: 'inherit', textDecoration: 'none' }}>{d.name}</Link></h2>
              <span className="muted small">{t('updates.count', { n: comps.length })}</span>
              {can('updates.run') && updatable.length > 0 && (
                <ConfirmButton className="btn sm" label={t('updates.updateAll')} confirmLabel={t('updates.confirmAll', { name: d.name })} onConfirm={() => start(d, ['*'])} />
              )}
            </div>
            <ComponentTable comps={comps.slice(0, 12)} />
            {comps.length > 12 && <Link to={`/devices/${d.id}`} className="small">{t('updates.more', { n: comps.length - 12 })}</Link>}
          </section>
        );
      })}
    </>
  );
}

function WarningPill({ c }: { c: Component }) {
  const t = useT();
  const w = c.meta?.warning as string | undefined;
  if (!w) return null;
  return <span className="pill warn" title={t('warnHint.' + w)}>{t('warn.' + w)}</span>;
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
  const [force, setForce] = useState(false);
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
  const update = (ids: string[]) => run(async () => { await post(`/api/devices/${device.id}/update`, { components: ids, force }); setSelected(new Set()); load(); });
  const scan = () => run(async () => { await post(`/api/devices/${device.id}/scan`); load(); });

  return (
    <section className="card">
      <div className="card-head">
        <h2 className="grow">{t('sw.title')}</h2>
        {sw.facts.scannedAt > 0 && <span className="small muted">{t('sw.scanned', { ago: formatAgo(lang, sw.facts.scannedAt) })}</span>}
        <button className="btn ghost sm" type="button" onClick={scan} disabled={busy || !!running}><IconRefresh />{t('sw.scanNow')}</button>
      </div>
      {running && <div className="alert info row"><Spinner /><span>{running.title}</span><Link to={`/jobs/${running.id}`} className="small">{t('sw.showLog')}</Link></div>}
      {sw.facts.error && <div className="alert error">{t('sw.scanFailed')}: {sw.facts.error}</div>}
      {facts.eolPassed && <div className="alert warn">{t('sw.eol', { date: facts.eol })}</div>}
      {facts.rebootRequired && <div className="alert warn">{t('sw.rebootRequired')}</div>}
      {Array.isArray(facts.warnings) && facts.warnings.map((w: string) => <div key={w} className="alert warn">{t('factWarn.' + w) !== 'factWarn.' + w ? t('factWarn.' + w) : w}</div>)}
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
              <button className="btn" type="button" disabled={busy || selected.size === 0 || !!running} onClick={() => update([...selected])}>{t('sw.updateSelected', { n: selected.size })}</button>
              <ConfirmButton className="btn ghost" label={t('sw.updateAll')} confirmLabel={t('updates.confirmAll', { name: device.name })} onConfirm={() => update(['*'])} disabled={busy || !!running} />
              {integ.capabilities.includes('busy') && <label className="check small"><input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />{t('sw.force')}</label>}
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
            <tr key={j.id} className="clickable" onClick={() => nav(`/jobs/${j.id}`)}>
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

function JobsPage() {
  return (
    <Routes>
      <Route index element={<JobsList />} />
      <Route path=":id" element={<JobDetail />} />
    </Routes>
  );
}

function JobsList() {
  const t = useT();
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const load = useCallback(() => { get<Job[]>('/api/jobs').then(setJobs).catch(() => {}); }, []);
  useEffect(() => { load(); }, [load]);
  useEvents({ 'job.status': load }, load);
  return (
    <>
      <div className="page-head"><h1>{t('jobs.title')}</h1></div>
      {!jobs ? <Spinner /> : jobs.length === 0 ? (
        <div className="card empty"><IconJobs /><h2>{t('jobs.emptyTitle')}</h2><p>{t('jobs.emptyText')}</p></div>
      ) : <JobList jobs={jobs} />}
    </>
  );
}

function JobDetail() {
  const t = useT();
  const lang = useLang();
  const { id } = useParams();
  const { can } = useSession();
  const [job, setJob] = useState<Job | null>(null);
  const load = useCallback(() => { get<Job>(`/api/jobs/${id}`).then(setJob).catch(() => {}); }, [id]);
  useEffect(() => { load(); }, [load]);
  useEvents({
    'job.log': (e: { id: number; text: string }) => { if (String(e.id) === id) setJob((j) => (j ? { ...j, log: (j.log ?? '') + e.text } : j)); },
    'job.status': (e: { id: number }) => { if (String(e.id) === id) load(); },
  }, load);
  if (!job) return <Spinner />;
  const live = job.status === 'running' || job.status === 'queued';
  return (
    <>
      <div className="page-head">
        <div className="grow stack" style={{ gap: 4 }}>
          <Link to="/jobs" className="small">{t('jobs.back')}</Link>
          <div className="row"><h1>{job.title}</h1><JobPill status={job.status} /></div>
        </div>
        {live && can('updates.run') && <button className="btn ghost" type="button" onClick={() => post(`/api/jobs/${job.id}/cancel`)}>{t('jobs.cancel')}</button>}
      </div>
      <p className="small muted">
        {t('jobs.meta', { by: job.createdBy || '–', when: formatTime(lang, job.createdAt) })}
        {job.deviceId && <> · <Link to={`/devices/${job.deviceId}`}>{job.deviceName}</Link></>}
      </p>
      {job.status === 'blocked' && job.result?.reason && <div className="alert warn">{t('jobs.blocked', { reason: job.result.reason })}</div>}
      <pre className="log">{job.log || t('jobs.noOutput')}</pre>
    </>
  );
}

// ---------------------------------------------------------------- integrations page

function IntegrationsPage() {
  const t = useT();
  const lang = useLang();
  const installed = useIntegrations();
  const [catalog, setCatalog] = useState<{ entries: CatalogEntry[]; errors: { source: string; error: string }[] } | null>(null);
  const [sources, setSources] = useState<string[]>([]);
  const [review, setReview] = useState<CatalogEntry | null>(null);
  const [setupId, setSetupId] = useState<string | null>(null);
  const setupInteg = installed.find((i) => i.id === setupId);
  const { error, run } = useAction();
  const loadCatalog = useCallback(() => {
    get<typeof catalog>('/api/integrations/catalog').then(setCatalog).catch(() => {});
    get<{ sources: string[] }>('/api/settings/catalog-sources').then((r) => setSources(r.sources)).catch(() => {});
  }, []);
  useEffect(() => { loadCatalog(); }, [loadCatalog]);

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
      <div className="page-head"><h1 className="grow">{t('market.title')}</h1></div>
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

      <SourcesCard sources={sources} onSaved={(s) => { setSources(s); loadCatalog(); }} />
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
  { path: 'jobs', labelKey: 'nav.jobs', perm: 'jobs.view', icon: <IconJobs />, element: <JobsPage /> },
  { path: 'integrations', labelKey: 'nav.integrations', perm: 'integrations.manage', icon: <IconPuzzle />, element: <IntegrationsPage /> },
);
// Software first, then which integration, then how to connect.
deviceSections.splice(0, 0, SoftwareCard, IntegrationCard);
