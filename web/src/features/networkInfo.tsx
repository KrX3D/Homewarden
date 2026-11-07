// Network card: how other devices (a UniFi console) see this device on the network.
// Shown on every device a console knows by MAC/IP; nothing to pick per device.
import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { get, type Device } from '../api';
import { formatAgo, useLang, useT } from '../i18n';
import { useEvents } from '../useEvents';
import { deviceSections } from '../extensions';

type Net = {
  source?: string; via?: 'wired' | 'wifi'; uplink?: string; port?: number; ssid?: string; signal?: number; channel?: number;
  radio?: string; txMbps?: number; rxMbps?: number; ip?: string; network?: string; vlan?: number | null; uptime?: number | null;
  alias?: string; hostname?: string;
};
type Report = { sourceId: number; sourceName: string; data: Net; updatedAt: number };

function duration(s: number) {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m`;
}

function NetworkCard({ device }: { device: Device }) {
  const t = useT();
  const lang = useLang();
  const [list, setList] = useState<Report[]>([]);
  const load = useCallback(() => { get<Report[]>(`/api/devices/${device.id}/network`).then(setList).catch(() => {}); }, [device.id]);
  useEffect(() => { load(); }, [load]);
  useEvents({ 'device.changed': load });
  if (list.length === 0) return null;
  return (
    <section className="card">
      <div className="card-head"><h2 className="grow">{t('netinfo.title')}</h2></div>
      {list.map((r) => {
        const n = r.data;
        const signal = n.signal != null ? (n.signal >= -60 ? 'ok' : n.signal >= -72 ? 'warn' : 'crit') : null;
        return (
          <div key={r.sourceId} className="stack" style={{ gap: 6 }}>
            <dl className="kv">
              <dt>{t('netinfo.connection')}</dt>
              <dd>{n.via === 'wired'
                ? t('netinfo.wired', { uplink: n.uplink || '?', port: n.port ?? '?' })
                : t('netinfo.wifi', { uplink: n.uplink || '?', ssid: n.ssid || '?' })}</dd>
              {n.signal != null && <><dt>{t('netinfo.signal')}</dt><dd><span className={'pill ' + signal}>{n.signal} dBm</span>
                {n.radio && <span className="muted small"> · {n.radio.toUpperCase()}</span>}{n.channel ? <span className="muted small"> · {t('netinfo.channel', { n: n.channel })}</span> : null}</dd></>}
              {(n.txMbps || n.rxMbps) ? <><dt>{t('netinfo.rate')}</dt><dd>{n.txMbps ?? '?'} / {n.rxMbps ?? '?'} Mbit/s</dd></> : null}
              {n.ip && <><dt>{t('netinfo.ip')}</dt><dd className="mono">{n.ip}</dd></>}
              {(n.network || n.vlan) && <><dt>{t('netinfo.network')}</dt><dd>{n.network}{n.vlan ? ` (VLAN ${n.vlan})` : ''}</dd></>}
              {n.uptime ? <><dt>{t('netinfo.connected')}</dt><dd>{duration(n.uptime)}</dd></> : null}
              {n.alias && n.alias !== device.name && <><dt>{t('netinfo.alias')}</dt><dd>{n.alias}</dd></>}
            </dl>
            <p className="small muted" style={{ margin: 0 }}>
              {t('netinfo.from', { source: n.source || '' })} <Link to={`/devices/${r.sourceId}`}>{r.sourceName}</Link> · {formatAgo(lang, r.updatedAt)}
            </p>
          </div>
        );
      })}
    </section>
  );
}

deviceSections.push(NetworkCard);
