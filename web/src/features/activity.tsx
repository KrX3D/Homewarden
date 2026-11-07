// Activity: everything Homewarden ran or is running (scans, updates, backups,
// restores) with filters, live progress and the full output of each run.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, Route, Routes, useNavigate, useParams } from 'react-router-dom';
import { get, post, type Job, type JobStatus } from '../api';
import { formatAgo, formatTime, useLang, useT, type Lang } from '../i18n';
import { useSession } from '../session';
import { useEvents } from '../useEvents';
import { Segmented, Spinner } from '../components/ui';
import { IconJobs } from '../components/icons';

const jobClass: Record<JobStatus, string> = { queued: 'off', running: 'accent', success: 'ok', failed: 'crit', blocked: 'warn', cancelled: 'off' };
export function JobPill({ status }: { status: JobStatus }) {
  const t = useT();
  return <span className={'pill dot ' + jobClass[status]}>{t('job.' + status)}</span>;
}

const PAGE = 200;
type Filter = 'all' | 'active' | 'failed' | 'blocked' | 'success';

function duration(j: Job): number | null {
  if (!j.startedAt) return null;
  return (j.finishedAt ?? Math.floor(Date.now() / 1000)) - j.startedAt;
}

export function formatDuration(s: number | null): string {
  if (s == null) return '–';
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
}

function dayLabel(lang: Lang, t: (k: string) => string, unix: number) {
  const d = new Date(unix * 1000);
  const today = new Date();
  const yest = new Date(); yest.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return t('activity.today');
  if (d.toDateString() === yest.toDateString()) return t('activity.yesterday');
  return d.toLocaleDateString(lang === 'de' ? 'de-DE' : 'en-GB', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
}

const clock = (lang: Lang, unix: number) => new Date(unix * 1000).toLocaleTimeString(lang === 'de' ? 'de-DE' : 'en-GB', { hour: '2-digit', minute: '2-digit' });

/** Why a job did not succeed, in one line. */
function reasonOf(j: Job): string {
  const r = j.result as { error?: string; reason?: string } | null;
  return (r && (r.error || r.reason)) || '';
}

function TypeChip({ type }: { type: string }) {
  const t = useT();
  const key = 'jobType.' + type;
  return <span className="chip">{t(key) !== key ? t(key) : type}</span>;
}

// ---------------------------------------------------------------- list

function ActivityList() {
  const t = useT();
  const lang = useLang();
  const nav = useNavigate();
  const { can } = useSession();
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [more, setMore] = useState(false);
  const [filter, setFilter] = useState<Filter>('all');
  const [type, setType] = useState('');
  const [device, setDevice] = useState('');
  const [query, setQuery] = useState('');
  const [lastLine, setLastLine] = useState<Record<number, string>>({});
  const [, tick] = useState(0);

  const load = useCallback(() => {
    get<Job[]>(`/api/jobs?limit=${PAGE}`).then((js) => {
      setJobs((cur) => {
        // Keep pages loaded with "older" when the newest page refreshes.
        if (!cur || cur.length <= PAGE) return js;
        const ids = new Set(js.map((j) => j.id));
        return [...js, ...cur.filter((j) => !ids.has(j.id) && j.id < js[js.length - 1].id)];
      });
      setMore((m) => m || js.length === PAGE);
    }).catch(() => {});
  }, []);
  useEffect(() => { load(); }, [load]);
  useEvents({
    'job.status': load,
    'job.log': (e: { id: number; text: string }) => {
      const line = e.text.trim().split('\n').pop() ?? '';
      setLastLine((c) => ({ ...c, [e.id]: line.replace(/^\d\d:\d\d:\d\d\s+/, '') }));
    },
  }, load);
  // Running durations count up.
  const anyActive = jobs?.some((j) => j.status === 'running' || j.status === 'queued');
  useEffect(() => {
    if (!anyActive) return;
    const id = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, [anyActive]);

  const older = () => {
    if (!jobs?.length) return;
    get<Job[]>(`/api/jobs?limit=${PAGE}&before=${jobs[jobs.length - 1].id}`).then((js) => {
      setJobs((cur) => [...(cur ?? []), ...js]);
      setMore(js.length === PAGE);
    }).catch(() => {});
  };

  const types = useMemo(() => [...new Set((jobs ?? []).map((j) => j.type))].sort(), [jobs]);
  const devices = useMemo(() => [...new Set((jobs ?? []).map((j) => j.deviceName).filter(Boolean))].sort((a, b) => a.localeCompare(b)), [jobs]);
  const dayAgo = Date.now() / 1000 - 86400;
  const active = (jobs ?? []).filter((j) => j.status === 'running' || j.status === 'queued');
  const failed24 = (jobs ?? []).filter((j) => j.status === 'failed' && j.createdAt > dayAgo).length;

  const shown = (jobs ?? []).filter((j) => {
    if (filter === 'active' && !(j.status === 'running' || j.status === 'queued')) return false;
    if (filter === 'failed' && !(j.status === 'failed' || j.status === 'cancelled')) return false;
    if (filter === 'blocked' && j.status !== 'blocked') return false;
    if (filter === 'success' && j.status !== 'success') return false;
    if (type && j.type !== type) return false;
    if (device && j.deviceName !== device) return false;
    const q = query.trim().toLowerCase();
    if (q && ![j.title, j.deviceName, j.createdBy, reasonOf(j)].some((x) => x?.toLowerCase().includes(q))) return false;
    return true;
  });
  // Running and queued jobs get their own block at the top of "all".
  const history = filter === 'all' ? shown.filter((j) => j.status !== 'running' && j.status !== 'queued') : shown;
  const groups: { day: string; jobs: Job[] }[] = [];
  for (const j of history) {
    const day = dayLabel(lang, t, j.createdAt);
    if (groups.length && groups[groups.length - 1].day === day) groups[groups.length - 1].jobs.push(j);
    else groups.push({ day, jobs: [j] });
  }
  const filtered = filter !== 'all' || !!type || !!device || query.trim() !== '';

  return (
    <>
      <div className="page-head">
        <h1 className="grow">{t('activity.title')}</h1>
        {active.length > 0 && <span className="pill dot accent">{t('activity.runningCount', { n: active.length })}</span>}
        {failed24 > 0 && <button type="button" className="pill dot crit" style={{ border: 0, cursor: 'pointer' }} onClick={() => setFilter('failed')}>{t('activity.failed24', { n: failed24 })}</button>}
      </div>
      <p className="muted" style={{ marginTop: -8 }}>{t('activity.intro')}</p>

      {!jobs ? <Spinner /> : jobs.length === 0 ? (
        <div className="card empty"><IconJobs /><h2>{t('jobs.emptyTitle')}</h2><p>{t('jobs.emptyText')}</p></div>
      ) : (
        <>
          <div className="row activity-filters">
            <Segmented label={t('jobs.status')} value={filter} onChange={setFilter} options={[
              { value: 'all', label: t('activity.f.all') },
              { value: 'active', label: t('activity.f.active') },
              { value: 'failed', label: t('activity.f.failed') },
              { value: 'blocked', label: t('activity.f.blocked') },
              { value: 'success', label: t('activity.f.success') },
            ]} />
            <select id="activity-type" aria-label={t('activity.type')} value={type} onChange={(e) => setType(e.target.value)}>
              <option value="">{t('activity.allTypes')}</option>
              {types.map((x) => <option key={x} value={x}>{t('jobType.' + x) !== 'jobType.' + x ? t('jobType.' + x) : x}</option>)}
            </select>
            <select id="activity-device" aria-label={t('activity.device')} value={device} onChange={(e) => setDevice(e.target.value)}>
              <option value="">{t('activity.allDevices')}</option>
              {devices.map((x) => <option key={x} value={x}>{x}</option>)}
            </select>
            <input id="activity-search" type="search" placeholder={t('activity.search')} value={query} onChange={(e) => setQuery(e.target.value)} />
            {filtered && <button className="btn subtle sm" type="button" onClick={() => { setFilter('all'); setType(''); setDevice(''); setQuery(''); }}>{t('activity.reset')}</button>}
          </div>

          {filter === 'all' && active.filter((j) => shown.includes(j)).length > 0 && (
            <section className="stack" style={{ gap: 8 }}>
              <h2 className="activity-day">{t('activity.now')}</h2>
              {active.filter((j) => shown.includes(j)).map((j) => (
                <div key={j.id} className="card activity-live clickable" onClick={() => nav(`/activity/${j.id}`)}>
                  <div className="row" style={{ flexWrap: 'nowrap' }}>
                    {j.status === 'running' ? <Spinner /> : null}
                    <strong className="grow ellipsis">{j.title}</strong>
                    <TypeChip type={j.type} />
                    <JobPill status={j.status} />
                  </div>
                  <div className="row small muted">
                    {j.deviceName && <span>{j.deviceName}</span>}
                    <span>{j.status === 'running' ? t('activity.runningFor', { d: formatDuration(duration(j)) }) : t('activity.waiting')}</span>
                    <span className="grow" />
                    {can('updates.run') && <button className="btn ghost sm" type="button" onClick={(e) => { e.stopPropagation(); post(`/api/jobs/${j.id}/cancel`); }}>{t('jobs.cancel')}</button>}
                  </div>
                  {lastLine[j.id] && <div className="mono small ellipsis activity-lastline">{lastLine[j.id]}</div>}
                </div>
              ))}
            </section>
          )}

          {groups.length === 0 && <p className="muted">{t('activity.noMatch')}</p>}
          {groups.map((g) => (
            <section key={g.day} className="stack" style={{ gap: 8 }}>
              <h2 className="activity-day">{g.day} <span className="muted small">({g.jobs.length})</span></h2>
              <div className="table-wrap">
                <table className="tbl activity-tbl">
                  <thead><tr><th style={{ width: 70 }}>{t('activity.time')}</th><th>{t('jobs.what')}</th><th>{t('activity.device')}</th><th>{t('jobs.by')}</th><th>{t('activity.duration')}</th><th>{t('jobs.status')}</th></tr></thead>
                  <tbody>{g.jobs.map((j) => {
                    const why = j.status === 'success' ? '' : reasonOf(j);
                    return (
                      <tr key={j.id} className="clickable" onClick={() => nav(`/activity/${j.id}`)}>
                        <td className="mono small" title={formatTime(lang, j.createdAt)}>{clock(lang, j.createdAt)}</td>
                        <td>
                          <div className="row" style={{ gap: 6, flexWrap: 'nowrap' }}><TypeChip type={j.type} /><span className="ellipsis">{j.title}</span></div>
                          {why && <div className={'small ellipsis ' + (j.status === 'blocked' ? 'text-warn' : 'text-crit')} style={{ maxWidth: 520 }} title={why}>{why}</div>}
                        </td>
                        <td className="small">{j.deviceId ? <Link to={`/devices/${j.deviceId}`} onClick={(e) => e.stopPropagation()}>{j.deviceName}</Link> : <span className="muted">–</span>}</td>
                        <td className="small muted">{j.createdBy || '–'}</td>
                        <td className="small mono">{formatDuration(duration(j))}</td>
                        <td><JobPill status={j.status} /></td>
                      </tr>
                    );
                  })}</tbody>
                </table>
              </div>
            </section>
          ))}
          {more && <div><button className="btn ghost" type="button" onClick={older}>{t('activity.older')}</button></div>}
        </>
      )}
    </>
  );
}

// ---------------------------------------------------------------- detail

function lineClass(l: string) {
  if (/\b(error|failed|fehler|fehlgeschlagen|refused|denied)\b/i.test(l)) return 'log-err';
  if (/\b(warn|warning|blocked|skipp?ed|not run)\b/i.test(l)) return 'log-warn';
  return undefined;
}

function ActivityDetail() {
  const t = useT();
  const lang = useLang();
  const { id } = useParams();
  const { can } = useSession();
  const [job, setJob] = useState<Job | null>(null);
  const [missing, setMissing] = useState(false);
  const [copied, setCopied] = useState(false);
  const [follow, setFollow] = useState(true);
  const [, tick] = useState(0);
  const pre = useRef<HTMLPreElement>(null);
  const load = useCallback(() => { get<Job>(`/api/jobs/${id}`).then(setJob).catch(() => setMissing(true)); }, [id]);
  useEffect(() => { load(); }, [load]);
  useEvents({
    'job.log': (e: { id: number; text: string }) => { if (String(e.id) === id) setJob((j) => (j ? { ...j, log: (j.log ?? '') + e.text } : j)); },
    'job.status': (e: { id: number }) => { if (String(e.id) === id) load(); },
  }, load);
  const live = job?.status === 'running' || job?.status === 'queued';
  useEffect(() => {
    if (!live) return;
    const i = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(i);
  }, [live]);
  // While the job runs, stay at the end of the output unless the user scrolled up.
  useEffect(() => { if (live && follow && pre.current) pre.current.scrollTop = pre.current.scrollHeight; }, [job?.log, live, follow]);

  if (missing) return <div className="card empty"><IconJobs /><h2>{t('activity.notFound')}</h2><p><Link to="/activity">{t('jobs.back')}</Link></p></div>;
  if (!job) return <Spinner />;
  const log = job.log || '';
  const lines = log ? log.replace(/\n$/, '').split('\n') : [];
  const problems = lines.filter((l) => lineClass(l) === 'log-err').length;
  const why = job.status === 'success' ? '' : reasonOf(job);
  const download = () => {
    const blob = new Blob([log], { type: 'text/plain' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `homewarden-${job.type}-${job.id}.log`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  };
  return (
    <>
      <div className="page-head">
        <div className="grow stack" style={{ gap: 4, minWidth: 0 }}>
          <Link to="/activity" className="small">{t('jobs.back')}</Link>
          <div className="row"><h1 style={{ overflowWrap: 'anywhere' }}>{job.title}</h1><JobPill status={job.status} /></div>
        </div>
        {live && can('updates.run') && <button className="btn ghost" type="button" onClick={() => post(`/api/jobs/${job.id}/cancel`)}>{t('jobs.cancel')}</button>}
      </div>

      <section className="card">
        <dl className="kv activity-meta">
          <dt>{t('activity.type')}</dt><dd><TypeChip type={job.type} /></dd>
          <dt>{t('activity.device')}</dt><dd>{job.deviceId ? <Link to={`/devices/${job.deviceId}`}>{job.deviceName}</Link> : '–'}</dd>
          <dt>{t('jobs.by')}</dt><dd>{job.createdBy || '–'}</dd>
          <dt>{t('activity.created')}</dt><dd>{formatTime(lang, job.createdAt)} <span className="muted small">({formatAgo(lang, job.createdAt)})</span></dd>
          {job.startedAt && <><dt>{t('activity.started')}</dt><dd>{formatTime(lang, job.startedAt)}</dd></>}
          {job.finishedAt && <><dt>{t('activity.finished')}</dt><dd>{formatTime(lang, job.finishedAt)}</dd></>}
          <dt>{t('activity.duration')}</dt><dd className="mono">{formatDuration(duration(job))}</dd>
        </dl>
      </section>

      {why && <div className={'alert ' + (job.status === 'blocked' ? 'warn' : 'error')}>{job.status === 'blocked' ? t('jobs.blocked', { reason: why }) : why}</div>}

      <section className="card stack" style={{ gap: 10 }}>
        <div className="card-head">
          <h2 className="grow">{t('activity.output')} <span className="muted small">{t('activity.lines', { n: lines.length })}{problems > 0 && <> · <span className="text-crit">{t('activity.problems', { n: problems })}</span></>}</span></h2>
          {live && <label className="check small"><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />{t('activity.follow')}</label>}
          <button className="btn subtle sm" type="button" disabled={!log} onClick={() => navigator.clipboard?.writeText(log).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500); }, () => {})}>{copied ? t('common.copied') : t('common.copy')}</button>
          <button className="btn subtle sm" type="button" disabled={!log} onClick={download}>{t('activity.download')}</button>
        </div>
        {lines.length === 0 ? <p className="muted small">{t('jobs.noOutput')}</p> : (
          <pre className="log activity-log" ref={pre}>{lines.map((l, i) => {
            const m = /^(\d\d:\d\d:\d\d)\s+(.*)$/.exec(l);
            return <span key={i} className={lineClass(l)}>{m ? <><span className="log-ts">{m[1]}</span>{'  '}{m[2]}</> : l}{'\n'}</span>;
          })}</pre>
        )}
      </section>
    </>
  );
}

export function ActivityPage() {
  return (
    <Routes>
      <Route index element={<ActivityList />} />
      <Route path=":id" element={<ActivityDetail />} />
    </Routes>
  );
}

