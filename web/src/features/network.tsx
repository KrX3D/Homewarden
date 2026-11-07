// What the firewall / VLAN rules must allow: core needs plus each installed
// integration's ports (from its manifest).
import { useEffect, useState } from 'react';
import { get, type Integration } from '../api';
import { useLang, useT } from '../i18n';
import { settingsTabs } from '../extensions';
import { tx } from './software';

export type Port = { target: 'device' | 'internet' | 'homewarden'; proto: string; port: string; note: Record<string, string> };

function Proto({ p }: { p: Port }) {
  return <span className="chip">{p.proto === 'icmp' ? 'ICMP' : `${p.proto.toUpperCase()} ${p.port}`}</span>;
}

/** The ports of one integration, grouped by direction. */
export function PortsList({ ports }: { ports?: Port[] }) {
  const t = useT();
  const lang = useLang();
  if (!ports || ports.length === 0) return null;
  const groups: Port['target'][] = ['device', 'internet', 'homewarden'];
  return (
    <div className="stack" style={{ gap: 6 }}>
      {groups.filter((g) => ports.some((p) => p.target === g)).map((g) => (
        <div key={g} className="stack" style={{ gap: 4 }}>
          <span className="small" style={{ fontWeight: 600 }}>{t('net.dir.' + g)}</span>
          {ports.filter((p) => p.target === g).map((p, i) => (
            <div className="row small" key={i} style={{ flexWrap: 'nowrap', alignItems: 'baseline' }}><Proto p={p} /><span className="muted">{tx(p.note, lang)}</span></div>
          ))}
        </div>
      ))}
    </div>
  );
}

const CORE: Port[] = [
  { target: 'homewarden', proto: 'tcp', port: '8080', note: { en: 'web interface; also agents and push monitors connect here (usually 443 through your reverse proxy)', de: 'Weboberfläche; auch Agents und Push-Monitore verbinden sich hierhin (meist 443 über deinen Reverse Proxy)' } },
  { target: 'device', proto: 'icmp', port: '', note: { en: 'online detection by ping, and the network scan', de: 'Online-Erkennung per Ping und der Netzwerk-Scan' } },
  { target: 'device', proto: 'tcp', port: '…', note: { en: 'whatever port you set as online check on a device (e.g. 22, 80)', de: 'der Port, den du bei einem Gerät als Online-Prüfung einstellst (z. B. 22, 80)' } },
  { target: 'device', proto: 'tcp', port: '445', note: { en: 'SMB backup target', de: 'SMB-Backup-Ziel' } },
  { target: 'device', proto: 'tcp', port: '1883', note: { en: 'MQTT broker, if used', de: 'MQTT-Broker, falls genutzt' } },
  { target: 'internet', proto: 'tcp', port: '443', note: { en: 'integration catalog (GitHub or your Gitea), notifications (Discord, Telegram, ntfy.sh), your SSO provider', de: 'Integrationskatalog (GitHub oder dein Gitea), Benachrichtigungen (Discord, Telegram, ntfy.sh), dein SSO-Anbieter' } },
];

function NetworkTab() {
  const t = useT();
  const lang = useLang();
  const [list, setList] = useState<Integration[]>([]);
  useEffect(() => { get<Integration[]>('/api/integrations').then(setList).catch(() => {}); }, []);
  return (
    <section className="stack">
      <p className="muted">{t('net.intro')}</p>
      <div className="alert info small">{t('net.vlan')}</div>
      <div className="grid-2">
        <section className="card">
          <h2>Homewarden</h2>
          <PortsList ports={CORE} />
        </section>
        {list.map((i) => (
          <section className="card" key={i.id}>
            <h2>{tx(i.name, lang)}</h2>
            {(i as Integration & { ports?: Port[] }).ports?.length ? <PortsList ports={(i as Integration & { ports?: Port[] }).ports} /> : <p className="small muted">{t('net.none')}</p>}
          </section>
        ))}
      </div>
    </section>
  );
}

settingsTabs.push({ path: 'network', labelKey: 'settings.network', element: <NetworkTab /> });
