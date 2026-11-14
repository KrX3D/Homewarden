// Overview: what is wrong now, what waits for the user, today's figures, each area's
// checks and the last seven days. Data from GET /api/overview.
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { get } from '../api';
import { formatAgo, useLang, useT } from '../i18n';
import { useEvents } from '../useEvents';
import { Spinner } from '../components/ui';
import { IconBackup, IconBattery, IconChevron, IconDevices, IconGit, IconGlobe, IconInbox, IconPuzzle, IconPulse, IconShield, IconUpdates } from '../components/icons';
import '../styles-overview.css';

type Area = 'servers' | 'network' | 'storage' | 'apps';
type Problem = { kind: 'monitor' | 'offline' | 'warning' | 'metric' | 'scan' | 'job'; severity: 'crit' | 'warn'; name: string; area: Area; code?: string; why?: string; since?: number; deviceId?: number; jobId?: number };
type Count = { up: number; total: number };
type Overview = {
  problems: Problem[];
  chores: { updates: number; security: number; updateDevices: number; inbox: number; integrationUpdates: number; reboot: string[];
    certs: { monitorId?: number; deviceId?: number; name: string; expires: number }[];
    batteries: { name: string; level: number | null; deviceId: number }[];
    prs: { count: number; bots: number; items: { repo: string; number: number; title: string; author: string; bot: boolean; url: string; forge: string }[] } };
  today: {
    dns: { instances: number; queries: number; blocked: number; avgMs: number | null; series: number[]; inSync: boolean; off: string[] } | null;
    internet: { device: string; status: string; isp: string; down: number | null; up: number | null; latency: number | null; speedtestAt: number | null } | null;
    proxy: { enabled: number; offline: number; certs: number; soonest: number } | null;
    pbs: { groups: number; fresh: number; verified: number; failed: number; last: number } | null;
    devices: Count; sometimes: number; monitors: Count; avgMs: number | null; uptime24h: number; msSeries: number[];
    guests: Count; containers: Count; updates: number; security: number; installedToday: number;
    backups: Count; lastBackup: number | null; disks: { device: string; mount: string; usedPct: number; sizeGB: number }[];
  };
  areas: { id: Area; health: 'up' | 'degraded' | 'down' | 'unknown'; up: number; total: number; ms: number | null; ticks: number[];
    items: { id: number; name: string; type: string; status: string; ms: number | null; ticks: number[] }[] }[];
  events: { kind: 'outage' | 'update' | 'backup' | 'failed'; area: Area; title: string; at: number; until?: number; deviceId?: number; jobId?: number }[];
  at: number;
};

const AREAS: Area[] = ['servers', 'network', 'storage', 'apps'];
const areaIcon: Record<Area, ReactNode> = {
  servers: <IconDevices />, network: <IconPulse />, storage: <IconBackup />, apps: <IconPuzzle />,
};

function problemLink(p: Problem) {
  if (p.kind === 'monitor') return '/monitors';
  if (p.kind === 'job') return '/activity';
  return p.deviceId ? `/devices/${p.deviceId}` : '/devices';
}

/** Reloads the overview on relevant events, at most every few seconds. */
function useOverview() {
  const [data, setData] = useState<Overview | null>(null);
  const timer = useRef<number | null>(null);
  const load = useCallback(() => { get<Overview>('/api/overview').then(setData).catch(() => {}); }, []);
  const soon = useCallback(() => {
    if (timer.current) return;
    timer.current = window.setTimeout(() => { timer.current = null; load(); }, 3000);
  }, [load]);
  useEffect(() => {
    load();
    const i = window.setInterval(load, 60000);
    return () => { clearInterval(i); if (timer.current) clearTimeout(timer.current); };
  }, [load]);
  useEvents({ 'monitor.state': soon, 'device.state': soon, 'device.software': soon, 'device.changed': soon, 'job.status': soon }, load);
  return data;
}

export function OverviewPage() {
  const t = useT();
  const data = useOverview();
  const { hash } = useLocation();
  const loaded = !!data;
  useEffect(() => {
    if (loaded && hash) document.getElementById(hash.slice(1))?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }, [loaded, hash]);
  if (!data) return <Spinner />;
  return (
    <div className="ov">
      <Hero data={data} />
      <div className="ov-split">
        <Problems list={data.problems} />
        <Chores c={data.chores} />
      </div>
      <Today d={data.today} />
      {data.areas.length > 0 && <Areas areas={data.areas} />}
      <Week events={data.events} at={data.at} />
      <p className="small muted" style={{ textAlign: 'right' }}>{t('ov.updated', { time: new Date(data.at * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) })}</p>
    </div>
  );
}

function Hero({ data }: { data: Overview }) {
  const t = useT();
  const crit = data.problems.filter((p) => p.severity === 'crit');
  const warn = data.problems.length - crit.length;
  const state = crit.length ? 'crit' : warn ? 'warn' : 'ok';
  const first = data.problems[0];
  const degraded = data.areas.filter((a) => a.health !== 'up' && a.health !== 'unknown').map((a) => t('ov.area.' + a.id) + ' ' + t('ov.health.' + a.health).toLowerCase());
  return (
    <section className={'ov-hero ' + state}>
      <div className="ov-hero-head">
        <div className="ov-hero-icon"><IconPulse /></div>
        <div>
          <h1>{crit.length ? tn(t, 'ov.problems', crit.length) : warn ? tn(t, 'ov.warnings', warn) : t('ov.allGood')}</h1>
          <p>{first ? [...degraded, `${first.name}: ${problemText(t, first)}`].join(' · ') : t('ov.allGoodText')}</p>
        </div>
      </div>
      <div className="ov-chips">
        {data.chores.updates > 0 && <Link className="btn ghost ov-chip go" to="/updates"><IconUpdates />{t('ov.installUpdates', { n: data.chores.updates })}</Link>}
        {data.chores.inbox > 0 && <Link className="btn ghost ov-chip" to="/devices/inbox"><IconInbox />{t('ov.inbox', { n: data.chores.inbox })}</Link>}
        <Link className="btn ghost ov-chip" to="/monitors"><IconPulse />{t('nav.monitors')}</Link>
        <Link className="btn ghost ov-chip" to="/devices"><IconDevices />{t('nav.devices')}</Link>
        <Link className="btn ghost ov-chip" to="/activity">{t('nav.activity')}</Link>
      </div>
    </section>
  );
}

type TFn = ReturnType<typeof useT>;

/** Singular text for n = 1 (key + "1"), else the plural key. */
const tn = (t: TFn, key: string, n: number) => t(n === 1 ? key + '1' : key, { n });

/** One line saying what is wrong: a known warning code, else the raw message. */
function problemText(t: TFn, p: Problem) {
  if (p.code) {
    const k = 'factWarn.' + p.code;
    const s = t(k);
    return s === k ? p.code : s;
  }
  if (p.kind === 'offline') return t('ov.kind.offline');
  return p.why || t('ov.kind.' + p.kind);
}

function Problems({ list }: { list: Problem[] }) {
  const t = useT();
  const lang = useLang();
  const crit = list.filter((p) => p.severity === 'crit').length;
  return (
    <section>
      <h2 className="ov-h">{t('ov.whatsWrong')} <span className="ov-n">{list.length ? `${tn(t, 'ov.problems', crit)} · ${tn(t, 'ov.warnings', list.length - crit)}` : ''}</span></h2>
      {list.length === 0 && <div className="ov-empty">{t('ov.nothingWrong')}</div>}
      {list.map((p, i) => (
        <Link key={i} to={problemLink(p)} className={'ov-prob ' + p.severity}>
          <span className="ov-prob-i">!</span>
          <span className="ov-prob-body">
            <span className="ov-prob-t">{p.name}</span>
            <span className="ov-prob-sub">{t('ov.kind.' + p.kind)} · {t('ov.area.' + p.area)}{p.since ? ' · ' + t('ov.since', { ago: formatAgo(lang, p.since) }) : ''}</span>
            {(p.code || p.why) && <span className={p.code ? 'ov-prob-text' : 'ov-prob-why'}>{problemText(t, p)}</span>}
            {p.code && p.why && <span className="ov-prob-why">{p.why}</span>}
          </span>
          <span className="ov-go">{t('ov.open')} ↗</span>
        </Link>
      ))}
    </section>
  );
}

function Chores({ c }: { c: Overview['chores'] }) {
  const t = useT();
  const rows: { to: string; href?: string; icon: ReactNode; label: string; extra?: ReactNode; n: number; sub?: boolean }[] = [];
  if (c.updates) rows.push({ to: '/updates', icon: <IconUpdates />, label: tn(t, 'ov.chore.updates', c.updateDevices), extra: c.security > 0 && <span className="ov-tag crit">{t('ov.security', { n: c.security })}</span>, n: c.updates });
  if (c.inbox) rows.push({ to: '/devices/inbox', icon: <IconInbox />, label: t('ov.chore.inbox'), n: c.inbox });
  if (c.integrationUpdates) rows.push({ to: '/integrations', icon: <IconPuzzle />, label: t('ov.chore.integrations'), n: c.integrationUpdates });
  if (c.reboot.length) rows.push({ to: '/devices', icon: <IconDevices />, label: t('ov.chore.reboot'), extra: <span className="muted small ellipsis">{c.reboot.join(', ')}</span>, n: c.reboot.length });
  if (c.prs.count) {
    rows.push({ to: '/devices', icon: <IconGit />, label: t('ov.chore.prs'), extra: c.prs.bots > 0 && <span className="ov-tag">{t('ov.bots', { n: c.prs.bots })}</span>, n: c.prs.count });
    for (const p of c.prs.items.slice(0, 4)) {
      rows.push({ to: '', href: p.url, icon: null, sub: true, label: `${p.repo} #${p.number} · ${p.title}`, extra: <span className="muted small">{p.author}</span>, n: 0 });
    }
  }
  if (c.batteries.length) {
    rows.push({ to: `/devices/${c.batteries[0].deviceId}`, icon: <IconBattery />, label: t('ov.chore.batteries'),
      extra: <span className="muted small ellipsis">{c.batteries.slice(0, 3).map((b) => b.name + (b.level != null ? ` ${b.level}%` : '')).join(', ')}</span>, n: c.batteries.length });
  }
  for (const cert of c.certs) {
    const days = Math.round((cert.expires - Date.now() / 1000) / 86400);
    rows.push({ to: cert.deviceId ? `/devices/${cert.deviceId}` : '/monitors', icon: <IconPulse />, label: t('ov.chore.cert', { name: cert.name }), extra: <span className={'ov-tag ' + (days < 7 ? 'crit' : 'warn')}>{days < 0 ? t('ov.expired') : t('ov.days', { n: days })}</span>, n: 1 });
  }
  return (
    <section>
      <h2 className="ov-h">{t('ov.waiting')}</h2>
      {rows.length === 0 ? <div className="ov-empty">{t('ov.nothingWaiting')}</div> : (
        <div className="ov-list">
          {rows.map((r, i) => {
            const body = (
              <>
                {r.sub ? <span className="ov-ri sub" /> : <span className="ov-ri">{r.icon}</span>}
                <span className="grow ellipsis">{r.label}</span>
                {r.extra}
                {!r.sub && <span className="ov-cnt">{r.n}</span>}
              </>
            );
            return r.href
              ? <a key={i} href={r.href} target="_blank" rel="noreferrer" className="ov-row sub">{body}</a>
              : <Link key={i} to={r.to} className="ov-row">{body}</Link>;
          })}
        </div>
      )}
    </section>
  );
}

function Share({ parts }: { parts: [number, string][] }) {
  return <div className="ov-share">{parts.filter(([n]) => n > 0).map(([n, c], i) => <i key={i} style={{ flex: n, background: c }} />)}</div>;
}

function Meter({ label, pct, color }: { label: string; pct: number; color?: string }) {
  return (
    <div className="ov-meter">
      <span className="ellipsis">{label}</span>
      <span className="ov-bar"><i style={{ width: Math.min(100, pct) + '%', background: color }} /></span>
      <span>{pct.toFixed(0)}%</span>
    </div>
  );
}

function fmtNum(lang: string, n: number) {
  return n.toLocaleString(lang === 'de' ? 'de-DE' : 'en-GB');
}

function Spark({ values, color }: { values: number[]; color?: string }) {
  if (values.length < 2) return null;
  const top = Math.max(...values) * 1.1 || 1;
  const d = values.map((v, i) => `${i ? 'L' : 'M'}${((i / (values.length - 1)) * 140).toFixed(1)} ${(26 - (v / top) * 24).toFixed(1)}`).join(' ');
  return <svg className="ov-spark" viewBox="0 0 140 28" preserveAspectRatio="none"><path d={d} style={color ? { stroke: color } : undefined} /></svg>;
}

function Tile({ icon, label, state, figure, unit, sub, children, to }: { icon: ReactNode; label: string; state?: 'warn' | 'crit'; figure: ReactNode; unit?: string; sub?: ReactNode; children?: ReactNode; to: string }) {
  return (
    <Link to={to} className="ov-tile">
      <span className={'ov-tile-l ' + (state ?? '')}>{icon}{label}</span>
      <span className="ov-fig">{figure}{unit && <small> {unit}</small>}</span>
      {sub && <span className="ov-tile-s">{sub}</span>}
      {children && <span className="ov-tile-b">{children}</span>}
    </Link>
  );
}

function Today({ d }: { d: Overview['today'] }) {
  const t = useT();
  const lang = useLang();
  const offline = d.devices.total - d.devices.up;
  return (
    <section>
      <h2 className="ov-h">{t('ov.today')}</h2>
      <div className="ov-tiles">
        <Tile to="/devices" icon={<IconDevices />} label={t('nav.devices')} state={offline - d.sometimes > 0 ? 'warn' : undefined} figure={d.devices.up} unit={t('ov.ofOnline', { n: d.devices.total })}
          sub={d.sometimes > 0 ? t('ov.sometimes', { n: d.sometimes }) : undefined}>
          <Share parts={[[d.devices.up, 'var(--ok)'], [d.sometimes, 'var(--off)'], [offline - d.sometimes, 'var(--crit)']]} />
        </Tile>
        <Tile to="/monitors" icon={<IconPulse />} label={t('nav.monitors')} state={d.monitors.up < d.monitors.total ? 'crit' : undefined} figure={d.monitors.up} unit={t('ov.ofUp', { n: d.monitors.total })}
          sub={[d.avgMs != null ? `Ø ${d.avgMs.toFixed(0)} ms` : '', d.uptime24h >= 0 ? t('ov.uptime', { pct: (d.uptime24h * 100).toFixed(1) }) : ''].filter(Boolean).join(' · ')}>
          <Spark values={d.msSeries} />
        </Tile>
        {d.guests.total > 0 && (
          <Tile to="/devices" icon={<IconDevices />} label={t('ov.guests')} state={d.guests.up < d.guests.total ? 'warn' : undefined} figure={d.guests.up} unit={t('ov.ofRunning', { n: d.guests.total })}>
            <Share parts={[[d.guests.up, 'var(--ov-servers)'], [d.guests.total - d.guests.up, 'var(--off)']]} />
          </Tile>
        )}
        {d.containers.total > 0 && (
          <Tile to="/devices" icon={<IconPuzzle />} label={t('ov.containers')} figure={d.containers.up} unit={t('ov.ofRunning', { n: d.containers.total })}>
            <Share parts={[[d.containers.up, 'var(--ov-apps)'], [d.containers.total - d.containers.up, 'var(--off)']]} />
          </Tile>
        )}
        <Tile to="/updates" icon={<IconUpdates />} label={t('nav.updates')} state={d.security > 0 ? 'crit' : undefined} figure={d.updates} unit={t('ov.pending')}
          sub={[d.security > 0 ? t('ov.security', { n: d.security }) : '', d.installedToday > 0 ? t('ov.installedToday', { n: d.installedToday }) : ''].filter(Boolean).join(' · ') || undefined}>
          <Share parts={[[d.security, 'var(--crit)'], [d.updates - d.security, 'var(--accent)'], [d.installedToday, 'var(--ok)']]} />
        </Tile>
        {d.pbs && d.pbs.groups > 0 && (
          <Tile to="/backups" icon={<IconBackup />} label={t('ov.pbs')} state={d.pbs.failed > 0 ? 'crit' : d.pbs.fresh < d.pbs.groups ? 'warn' : undefined} figure={d.pbs.fresh} unit={t('ov.ofCurrent', { n: d.pbs.groups })}
            sub={[d.pbs.last ? t('ov.lastBackup', { ago: formatAgo(lang, d.pbs.last) }) : '', d.pbs.failed ? t('ov.verifyFailed', { n: d.pbs.failed }) : t('ov.verified', { n: d.pbs.verified })].filter(Boolean).join(' · ')}>
            <Share parts={[[d.pbs.verified, 'var(--ov-info)'], [d.pbs.groups - d.pbs.verified - d.pbs.failed, 'var(--off)'], [d.pbs.failed, 'var(--crit)']]} />
          </Tile>
        )}
        {d.dns && (
          <Tile to="/devices" icon={<IconShield />} label={t('ov.dns')} state={d.dns.off.length || !d.dns.inSync ? 'warn' : undefined} figure={fmtNum(lang, d.dns.queries)} unit={t('ov.queries')}
            sub={[t('ov.blocked', { n: fmtNum(lang, d.dns.blocked), pct: d.dns.queries ? Math.round((100 * d.dns.blocked) / d.dns.queries) : 0 }),
              d.dns.instances > 1 ? (d.dns.inSync ? t('ov.inSync', { n: d.dns.instances }) : t('ov.outOfSync')) : '',
              d.dns.off.length ? t('ov.protectionOff', { names: d.dns.off.join(', ') }) : ''].filter(Boolean).join(' · ')}>
            <Spark values={d.dns.series} color="var(--ov-network)" />
          </Tile>
        )}
        {d.internet && (
          <Tile to="/devices" icon={<IconGlobe />} label={t('ov.internet')} state={d.internet.status && d.internet.status !== 'ok' ? 'crit' : undefined}
            figure={d.internet.down != null ? Math.round(d.internet.down) : d.internet.status === 'ok' ? 'OK' : '–'} unit={d.internet.down != null ? 'Mbit/s ↓' : ''}
            sub={[d.internet.up != null ? Math.round(d.internet.up) + ' Mbit/s ↑' : '', d.internet.latency != null ? d.internet.latency + ' ms' : '', d.internet.isp,
              d.internet.speedtestAt ? t('ov.speedtest', { ago: formatAgo(lang, d.internet.speedtestAt) }) : ''].filter(Boolean).join(' · ')} />
        )}
        {d.proxy && (
          <Tile to="/devices" icon={<IconGlobe />} label={t('ov.proxy')} state={d.proxy.offline ? 'warn' : undefined} figure={d.proxy.enabled - d.proxy.offline} unit={t('ov.ofOnline', { n: d.proxy.enabled })}
            sub={d.proxy.certs ? t('ov.certsNext', { n: d.proxy.certs, days: Math.max(0, Math.round((d.proxy.soonest - Date.now() / 1000) / 86400)) }) : undefined}>
            <Share parts={[[d.proxy.enabled - d.proxy.offline, 'var(--ok)'], [d.proxy.offline, 'var(--crit)']]} />
          </Tile>
        )}
        {d.backups.total > 0 && (
          <Tile to="/backups" icon={<IconBackup />} label={t('nav.backups')} state={d.backups.up < d.backups.total ? 'warn' : undefined} figure={d.backups.up} unit={t('ov.ofCurrent', { n: d.backups.total })}
            sub={d.lastBackup ? t('ov.lastBackup', { ago: formatAgo(lang, d.lastBackup) }) : undefined}>
            <Share parts={[[d.backups.up, 'var(--ov-info)'], [d.backups.total - d.backups.up, 'var(--warn)']]} />
          </Tile>
        )}
        {d.disks.length > 0 && (
          <Tile to="/devices" icon={<IconBackup />} label={t('ov.storage')} state={d.disks[0].usedPct >= 90 ? 'warn' : undefined} figure={d.disks[0].usedPct.toFixed(0)} unit={'% ' + d.disks[0].mount}
            sub={`${d.disks[0].device}${d.disks[0].sizeGB ? ' · ' + (d.disks[0].sizeGB >= 1000 ? (d.disks[0].sizeGB / 1000).toFixed(1) + ' TB' : d.disks[0].sizeGB.toFixed(0) + ' GB') : ''}`}>
            {d.disks.slice(0, 2).map((k, i) => <Meter key={i} label={k.mount} pct={k.usedPct} color="var(--ov-storage)" />)}
          </Tile>
        )}
      </div>
    </section>
  );
}

function Ticks({ ticks }: { ticks: number[] }) {
  const pad = Array.from({ length: Math.max(0, 30 - ticks.length) }, () => -1);
  return <span className="ov-ticks">{[...pad, ...ticks].map((v, i) => <i key={i} className={v === 0 ? 'f' : v === 1 ? 'o' : v === 2 ? 'p' : ''} />)}</span>;
}

function Health({ h }: { h: string }) {
  const t = useT();
  const cls = h === 'up' ? 'ok' : h === 'degraded' || h === 'dependency' ? 'warn' : h === 'down' ? 'crit' : 'off';
  const mark = cls === 'ok' ? '●' : cls === 'warn' ? '◐' : cls === 'crit' ? '■' : '○';
  return <span className={'ov-health ' + cls}>{mark} {t(h === 'dependency' ? 'mon.status.dependency' : 'ov.health.' + h)}</span>;
}

function Areas({ areas }: { areas: Overview['areas'] }) {
  const t = useT();
  const [open, setOpen] = useState<Record<string, boolean>>({});
  return (
    <section>
      <h2 className="ov-h">{t('ov.areas')} <span className="ov-n">{t('ov.last30')}</span></h2>
      <div className="ov-tbl">
        <div className="ov-tr ov-th"><span>{t('cred.name')}</span><span>{t('ov.health')}</span><span>{t('ov.checks')}</span><span>{t('mon.response')}</span><span>{t('ov.results')}</span></div>
        {areas.map((a) => (
          <div key={a.id} id={'area-' + a.id}>
            <button type="button" className="ov-tr" onClick={() => setOpen({ ...open, [a.id]: !open[a.id] })} aria-expanded={!!open[a.id]}>
              <span className="ov-nm"><span className={'ov-chev' + (open[a.id] ? ' open' : '')}><IconChevron /></span><span className={'ov-ai ' + a.id}>{areaIcon[a.id]}</span>{t('ov.area.' + a.id)}</span>
              <Health h={a.health} />
              <span className="ov-num">{a.up} / {a.total}</span>
              <span className="mono small">{a.ms != null ? a.ms.toFixed(0) + ' ms' : '–'}</span>
              <Ticks ticks={a.ticks} />
            </button>
            {open[a.id] && a.items.map((it) => (
              <Link key={it.id} to="/monitors" className="ov-tr child">
                <span className="ov-nm">{it.name}</span>
                <Health h={it.status} />
                <span className="muted small">{t('monType.' + it.type)}</span>
                <span className="mono small">{it.ms != null && it.status === 'up' ? it.ms.toFixed(0) + ' ms' : '–'}</span>
                <Ticks ticks={it.ticks} />
              </Link>
            ))}
          </div>
        ))}
      </div>
    </section>
  );
}

function Week({ events, at }: { events: Overview['events']; at: number }) {
  const t = useT();
  const lang = useLang();
  const from = at - 7 * 86400;
  const pos = (x: number) => Math.max(0, Math.min(100, ((x - from) / (at - from)) * 100));
  const days = Array.from({ length: 7 }, (_, i) => new Date((from + (i + 0.5) * 86400) * 1000).toLocaleDateString(lang === 'de' ? 'de-DE' : 'en-GB', { weekday: 'short' }));
  days[6] = t('ov.todayShort');
  const when = (x: number) => new Date(x * 1000).toLocaleString(lang === 'de' ? 'de-DE' : 'en-GB', { weekday: 'short', hour: '2-digit', minute: '2-digit' });
  const lanes = AREAS.filter((a) => events.some((e) => e.area === a));
  return (
    <section>
      <h2 className="ov-h">{t('ov.week')}</h2>
      <div className="ov-week">
        {lanes.length === 0 ? <div className="muted small">{t('ov.quietWeek')}</div> : (
          <>
            <div className="ov-days"><span />{days.map((d, i) => <span key={i}>{d}</span>)}</div>
            {lanes.map((a) => (
              <div className="ov-wk" key={a}>
                <span>{t('ov.area.' + a)}</span>
                <div className="ov-lane">
                  {events.filter((e) => e.area === a).map((e, i) => e.kind === 'outage'
                    ? <span key={i} className="ov-out" title={`${e.title}: ${when(e.at)}`} style={{ left: pos(e.at) + '%', width: Math.max(0.6, pos(e.until ?? at) - pos(e.at)) + '%' }} />
                    : <span key={i} className={'ov-ev ' + e.kind} title={`${e.title}: ${when(e.at)}`} style={{ left: `calc(${pos(e.at)}% - 5px)` }} />)}
                </div>
              </div>
            ))}
            <div className="ov-legend"><span><i className="out" />{t('ov.ev.outage')}</span><span><i className="update" />{t('ov.ev.update')}</span><span><i className="backup" />{t('ov.ev.backup')}</span><span><i className="failed" />{t('ov.ev.failed')}</span></div>
          </>
        )}
        {events.length > 0 && (
          <div className="ov-feed">
            {events.slice(0, 12).map((e, i) => (
              <Link key={i} className="ov-fe" to={e.kind === 'outage' ? '/monitors' : '/activity'}>
                <span className="muted">{when(e.at)}</span>
                <span className={'ov-mk ' + e.kind} />
                <span>
                  {e.kind === 'outage' ? <><b>{e.title}</b> {e.until ? t('ov.outageFor', { min: Math.max(1, Math.round((e.until - e.at) / 60)) }) : t('ov.outageNow')}</> : <><b>{e.title}</b>{e.kind === 'failed' ? ' · ' + t('ov.ev.failed') : ''}</>}
                </span>
              </Link>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}

type NavData = { areas: { id: Area; health: string; down: number }[]; apps: { id: number; name: string; url: string; status: string }[] };

/** Sidebar sections: health per area (links into the overview) and web apps with their state. */
export function SidebarStatus() {
  const t = useT();
  const [data, setData] = useState<NavData | null>(null);
  const [closed, setClosed] = useState<Record<string, boolean>>(() => { try { return JSON.parse(localStorage.getItem('hw.sidebar.closed') || '{}'); } catch { return {}; } });
  const load = useCallback(() => { get<NavData>('/api/overview/nav').then(setData).catch(() => {}); }, []);
  useEffect(() => { load(); const i = window.setInterval(load, 60000); return () => clearInterval(i); }, [load]);
  useEvents({ 'monitor.state': load }, load);
  if (!data || (data.areas.length === 0 && data.apps.length === 0)) return null;
  const toggle = (k: string) => {
    const next = { ...closed, [k]: !closed[k] };
    setClosed(next);
    try { localStorage.setItem('hw.sidebar.closed', JSON.stringify(next)); } catch { /* private mode */ }
  };
  const dot = (s: string) => 'sb-dot ' + (s === 'up' ? 'ok' : s === 'degraded' || s === 'dependency' ? 'warn' : s === 'down' ? 'crit' : 'off');
  return (
    <div className="sb-status">
      {data.areas.length > 0 && (
        <>
          <button type="button" className="sb-sec" onClick={() => toggle('areas')} aria-expanded={!closed.areas}>{t('ov.areas')}<span className={'ov-chev' + (closed.areas ? '' : ' open')}><IconChevron /></span></button>
          {!closed.areas && data.areas.map((a) => (
            <Link key={a.id} to={'/overview#area-' + a.id} className="sb-link"><span className={'ov-ai ' + a.id}>{areaIcon[a.id]}</span><span className="grow">{t('ov.area.' + a.id)}</span><span className={dot(a.health)} /></Link>
          ))}
        </>
      )}
      {data.apps.length > 0 && (
        <>
          <button type="button" className="sb-sec" onClick={() => toggle('apps')} aria-expanded={!closed.apps}>{t('ov.area.apps')}<span className={'ov-chev' + (closed.apps ? '' : ' open')}><IconChevron /></span></button>
          {!closed.apps && data.apps.map((a) => (
            <a key={a.id} href={a.url} target="_blank" rel="noreferrer" className="sb-link" title={a.url}>
              <span className="sb-app" style={{ background: appColor(a.name) }}>{a.name.replace(/[^A-Za-z0-9]/g, '').slice(0, 2).toUpperCase()}</span>
              <span className="grow ellipsis">{a.name}</span><span className={dot(a.status)} />
            </a>
          ))}
        </>
      )}
    </div>
  );
}

function appColor(name: string) {
  let h = 0;
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) % 360;
  return `hsl(${h} 55% 42%)`;
}
