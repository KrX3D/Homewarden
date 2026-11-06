// Resource history card on the device page: CPU/RAM chart, file systems, temperatures,
// SMART and per-container usage.
import { useCallback, useEffect, useState } from 'react';
import { get, type Device } from '../api';
import { formatAgo, useLang, useT } from '../i18n';
import { useEvents } from '../useEvents';
import { Segmented } from '../components/ui';
import { deviceSections } from '../extensions';

type Sample = {
  cpu?: number | null; memPct?: number | null; memUsedMB?: number | null; memTotalMB?: number | null; load1?: number | null;
  disks?: { mount: string; usedPct: number; sizeGB?: number }[]; temps?: { name: string; c: number }[];
  smart?: { dev: string; model?: string; healthy?: boolean | null; tempC?: number | null; hours?: number | null; reallocated?: number | null; pending?: number | null; wearPct?: number | null; mediaErrors?: number | null }[];
  containers?: { name: string; cpu: number | null; memMB: number | null }[];
};
type Point = { at: number; data: Sample };

function Chart({ points, hours }: { points: Point[]; hours: number }) {
  const t = useT();
  const W = 600, H = 130, P = 30;
  const now = Date.now() / 1000, from = now - hours * 3600;
  const x = (at: number) => P + ((at - from) / (now - from)) * (W - P - 6);
  const y = (v: number) => H - 18 - (v / 100) * (H - 28);
  const path = (key: 'cpu' | 'memPct') => points.filter((p) => p.data[key] != null)
    .map((p, i) => `${i ? 'L' : 'M'}${x(p.at).toFixed(1)},${y(p.data[key] as number).toFixed(1)}`).join(' ');
  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} className="chart" role="img" aria-label="CPU and memory">
        {[0, 50, 100].map((v) => <g key={v}><line x1={P} x2={W} y1={y(v)} y2={y(v)} className="grid" /><text x={P - 4} y={y(v) + 3} textAnchor="end" className="axis">{v}%</text></g>)}
        <path d={path('memPct')} className="line-2" />
        <path d={path('cpu')} className="line" />
      </svg>
      <div className="row small muted" style={{ gap: 14 }}>
        <span className="row" style={{ gap: 5 }}><span className="swatch accent" />CPU</span>
        <span className="row" style={{ gap: 5 }}><span className="swatch ok" />{t('res.memory')}</span>
      </div>
    </div>
  );
}

function Meter({ pct }: { pct: number }) {
  const cls = pct >= 90 ? 'crit' : pct >= 75 ? 'warn' : 'ok';
  return <span className="meter"><span className={'meter-' + cls} style={{ width: Math.min(100, pct) + '%' }} /></span>;
}

function ResourcesCard({ device }: { device: Device }) {
  const t = useT();
  const lang = useLang();
  const [hours, setHours] = useState<'24' | '168' | '720'>('24');
  const [points, setPoints] = useState<Point[] | null>(null);
  const load = useCallback(() => { get<Point[]>(`/api/devices/${device.id}/metrics?hours=${hours}`).then(setPoints).catch(() => setPoints([])); }, [device.id, hours]);
  useEffect(() => { load(); }, [load]);
  useEvents({ 'device.metrics': (e: { id: number }) => e.id === device.id && load() });
  if (!points || points.length === 0) return null;
  const last = points[points.length - 1];
  const d = last.data;
  const smart = [...points].reverse().find((p) => p.data.smart && p.data.smart.length)?.data.smart ?? [];
  const hasHost = d.cpu != null || d.memPct != null;
  return (
    <section className="card">
      <div className="card-head">
        <h2 className="grow">{t('res.title')}</h2>
        <span className="small muted">{formatAgo(lang, last.at)}</span>
        {hasHost && <Segmented label={t('mon.range')} value={hours} onChange={setHours} options={[{ value: '24', label: '24h' }, { value: '168', label: '7d' }, { value: '720', label: '30d' }]} />}
      </div>
      {hasHost && (
        <>
          <div className="tiles-row">
            {d.cpu != null && <div className="tile"><div className="v">{Math.round(d.cpu)}%</div><div className="l">CPU</div></div>}
            {d.memPct != null && <div className="tile"><div className="v">{d.memPct}%</div><div className="l">{t('res.memory')} · {((d.memUsedMB ?? 0) / 1024).toFixed(1)} / {((d.memTotalMB ?? 0) / 1024).toFixed(1)} GB</div></div>}
            {d.load1 != null && <div className="tile"><div className="v">{d.load1}</div><div className="l">{t('res.load')}</div></div>}
            {(d.temps ?? []).slice(0, 2).map((x) => <div className="tile" key={x.name}><div className="v">{x.c.toFixed(0)} °C</div><div className="l">{x.name}</div></div>)}
          </div>
          <Chart points={points} hours={Number(hours)} />
        </>
      )}
      {(d.disks ?? []).length > 0 && (
        <div className="stack" style={{ gap: 6 }}>
          <strong className="small">{t('res.disks')}</strong>
          {d.disks!.map((x) => (
            <div className="row small" key={x.mount} style={{ flexWrap: 'nowrap' }}>
              <span className="mono ellipsis" style={{ width: 170 }}>{x.mount}</span><Meter pct={x.usedPct} /><span className="mono" style={{ width: 90, textAlign: 'right' }}>{x.usedPct}%{x.sizeGB ? ` / ${x.sizeGB} GB` : ''}</span>
            </div>
          ))}
        </div>
      )}
      {smart.length > 0 && (
        <div className="table-wrap">
          <table className="tbl">
            <thead><tr><th>SMART</th><th>{t('res.health')}</th><th>°C</th><th>{t('res.hours')}</th><th>{t('res.realloc')}</th><th>{t('res.wear')}</th></tr></thead>
            <tbody>{smart.map((s) => (
              <tr key={s.dev}>
                <td className="small"><span className="mono">{s.dev}</span> <span className="muted">{s.model}</span></td>
                <td>{s.healthy == null ? '–' : <span className={'pill dot ' + (s.healthy ? 'ok' : 'crit')}>{s.healthy ? t('res.passed') : t('res.failed')}</span>}</td>
                <td className="mono small">{s.tempC ?? '–'}</td>
                <td className="mono small">{s.hours != null ? s.hours.toLocaleString() : '–'}</td>
                <td className="mono small">{s.reallocated != null || s.pending != null ? `${s.reallocated ?? 0} / ${s.pending ?? 0}` : s.mediaErrors != null ? s.mediaErrors : '–'}</td>
                <td className="mono small">{s.wearPct != null ? s.wearPct + '%' : '–'}</td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      )}
      {(d.containers ?? []).length > 0 && (
        <div className="table-wrap">
          <table className="tbl">
            <thead><tr><th>{t('res.container')}</th><th>CPU</th><th>{t('res.memory')}</th></tr></thead>
            <tbody>{[...d.containers!].sort((a, b) => (b.cpu ?? 0) - (a.cpu ?? 0)).map((c) => (
              <tr key={c.name}><td>{c.name}</td><td className="mono small">{c.cpu != null ? c.cpu.toFixed(1) + '%' : '–'}</td><td className="mono small">{c.memMB != null ? c.memMB + ' MB' : '–'}</td></tr>
            ))}</tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// Right after the Software card.
deviceSections.splice(1, 0, ResourcesCard);
