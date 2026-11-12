// Notification channels and settings (Settings → Notifications).
import { useEffect, useState, type FormEvent } from 'react';
import { del, get, post, put } from '../api';
import { formatAgo, useLang, useT } from '../i18n';
import { ConfirmButton, ErrorAlert, Modal, useAction } from '../components/ui';
import { IconEdit, IconPlus } from '../components/icons';
import { settingsTabs } from '../extensions';

type ChannelType = 'discord' | 'ntfy' | 'telegram' | 'gotify' | 'email' | 'webhook';
type Config = { url?: string; topic?: string; token?: string; chatId?: string; host?: string; port?: number; username?: string; password?: string; from?: string; to?: string };
type Channel = { id: number; name: string; type: ChannelType; events: string[]; tags: string[]; enabled: boolean; summary: string };
type Data = { channels: Channel[]; settings: { lang: string; baseUrl: string }; events: string[] };

const FIELDS: Record<ChannelType, (keyof Config)[]> = {
  discord: ['url'], webhook: ['url'], ntfy: ['url', 'topic', 'token'], gotify: ['url', 'token'],
  telegram: ['token', 'chatId'], email: ['host', 'port', 'username', 'password', 'from', 'to'],
};
const SECRET: (keyof Config)[] = ['token', 'password'];

function NotificationsTab() {
  const t = useT();
  const [data, setData] = useState<Data | null>(null);
  const [editing, setEditing] = useState<Channel | 'new' | null>(null);
  const [settings, setSettings] = useState({ lang: 'en', baseUrl: '' });
  const [saved, setSaved] = useState(false);
  const { busy, error, run } = useAction();
  const load = () => get<Data>('/api/notify').then((d) => { setData(d); setSettings(d.settings); }).catch(() => {});
  useEffect(() => { load(); }, []);
  if (!data) return null;
  return (
    <section className="stack">
      <p className="muted">{t('notify.intro')}</p>
      <form className="card" onSubmit={(e) => { e.preventDefault(); setSaved(false); run(async () => { await put('/api/notify/settings', settings); setSaved(true); }); }}>
        <div className="row" style={{ alignItems: 'flex-end' }}>
          <label className="field" style={{ flex: '0 1 160px' }}>{t('account.language')}
            <select id="notify-lang" value={settings.lang} onChange={(e) => setSettings({ ...settings, lang: e.target.value })}>
              <option value="en">English</option><option value="de">Deutsch</option>
            </select>
          </label>
          <label className="field" style={{ flex: '1 1 260px' }}>{t('notify.baseUrl')}
            <input id="notify-base" type="url" value={settings.baseUrl} placeholder={location.origin} onChange={(e) => setSettings({ ...settings, baseUrl: e.target.value })} />
          </label>
          <button className="btn ghost" type="submit" disabled={busy}>{t('common.save')}</button>
        </div>
        <span className="small muted">{t('notify.baseUrlHint')}</span>
        {saved && <div className="alert ok">{t('conn.saved')}</div>}
      </form>
      <div className="page-head">
        <h2 className="grow">{t('notify.channels')}</h2>
        <button className="btn" type="button" onClick={() => setEditing('new')}><IconPlus />{t('notify.add')}</button>
      </div>
      <ErrorAlert error={error} />
      {data.channels.length === 0 ? <p className="muted">{t('notify.none')}</p> : (
        <div className="grid-2">
          {data.channels.map((c) => (
            <div className="card" key={c.id}>
              <div className="card-head">
                <h3 className="grow">{c.name}</h3>
                <span className="chip">{t('notifyType.' + c.type)}</span>
                {!c.enabled && <span className="pill off">{t('common.off')}</span>}
                <button className="btn subtle sm" type="button" aria-label={t('common.edit')} onClick={() => setEditing(c)}><IconEdit /></button>
              </div>
              <span className="small mono muted">{c.summary}</span>
              <div className="row" style={{ gap: 6 }}>{c.events.map((e) => <span key={e} className="chip">{t('notifyEv.' + e)}</span>)}{c.tags.map((x) => <span key={x} className="chip">#{x}</span>)}</div>
            </div>
          ))}
        </div>
      )}
      {editing && <ChannelModal channel={editing === 'new' ? null : editing} events={data.events} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
      <WatchCard channels={data.channels} />
    </section>
  );
}

type WatchData = {
  heartbeat: { url: string; minutes: number };
  status: { at: number; error: string };
  agents: { deviceIds: number[]; channelId: number; minutes: number; host: string };
  agentList: { deviceId: number; name: string; hostname: string; version: string; lastSeen: number | null; online: boolean }[];
};

/** Watching Homewarden from outside: a heartbeat URL, and agents that alert on their own. */
function WatchCard({ channels }: { channels: Channel[] }) {
  const t = useT();
  const lang = useLang();
  const [data, setData] = useState<WatchData | null>(null);
  const [saved, setSaved] = useState(false);
  const { busy, error, run } = useAction();
  useEffect(() => { get<WatchData>('/api/settings/watch').then(setData).catch(() => {}); }, []);
  if (!data) return null;
  const hb = data.heartbeat, ag = data.agents;
  const setHb = (p: Partial<WatchData['heartbeat']>) => { setSaved(false); setData({ ...data, heartbeat: { ...hb, ...p } }); };
  const setAg = (p: Partial<WatchData['agents']>) => { setSaved(false); setData({ ...data, agents: { ...ag, ...p } }); };
  const save = () => run(async () => { setData(await put<WatchData>('/api/settings/watch', { heartbeat: hb, agents: ag })); setSaved(true); });
  return (
    <form className="card stack" onSubmit={(e) => { e.preventDefault(); save(); }}>
      <h2>{t('watch.title')}</h2>
      <p className="small muted">{t('watch.intro')}</p>

      <h3>{t('watch.hbTitle')}</h3>
      <p className="small muted" style={{ margin: 0 }}>{t('watch.hbIntro')}</p>
      <div className="row" style={{ alignItems: 'flex-end' }}>
        <label className="field" style={{ flex: '1 1 320px' }}>{t('watch.hbUrl')}
          <input id="watch-hb-url" type="url" className="mono" value={hb.url} placeholder="https://hc-ping.com/…" onChange={(e) => setHb({ url: e.target.value })} />
        </label>
        <label className="field" style={{ flex: '0 1 140px' }}>{t('watch.every')}
          <input id="watch-hb-min" type="number" min={1} max={60} value={hb.minutes} onChange={(e) => setHb({ minutes: Number(e.target.value) })} />
        </label>
        <button className="btn ghost" type="button" disabled={busy || !hb.url} onClick={() => run(async () => { await put('/api/settings/watch', { heartbeat: hb, agents: ag }); const st = await post<WatchData['status']>('/api/settings/watch/ping'); setData({ ...data, status: st }); })}>{t('watch.pingNow')}</button>
      </div>
      {data.status.at > 0 && (
        <span className={'small' + (data.status.error ? '' : ' muted')} style={data.status.error ? { color: 'var(--crit)' } : undefined}>
          {data.status.error ? t('watch.hbFailed', { ago: formatAgo(lang, data.status.at), err: data.status.error }) : t('watch.hbOk', { ago: formatAgo(lang, data.status.at) })}
        </span>
      )}

      <h3>{t('watch.agentTitle')}</h3>
      <p className="small muted" style={{ margin: 0 }}>{t('watch.agentIntro')}</p>
      {data.agentList.length === 0 ? <p className="small muted">{t('watch.noAgents')}</p> : (
        <div className="stack" style={{ gap: 6 }}>
          {data.agentList.map((a) => (
            <label className="check" key={a.deviceId}>
              <input type="checkbox" checked={ag.deviceIds.includes(a.deviceId)} onChange={(e) => setAg({ deviceIds: e.target.checked ? [...ag.deviceIds, a.deviceId] : ag.deviceIds.filter((x) => x !== a.deviceId) })} />
              <span>{a.name} <span className="muted small">· {a.hostname} · {a.online ? t('watch.online') : t('watch.offline')}{a.version ? ' · ' + a.version : ''}</span></span>
            </label>
          ))}
        </div>
      )}
      <div className="row" style={{ alignItems: 'flex-start' }}>
        <label className="field" style={{ flex: '1 1 180px' }}>{t('watch.channel')}
          <select id="watch-channel" value={ag.channelId || ''} onChange={(e) => setAg({ channelId: Number(e.target.value) || 0 })}>
            <option value="">–</option>
            {channels.filter((c) => c.type !== 'email').map((c) => <option key={c.id} value={c.id}>{c.name} ({t('notifyType.' + c.type)})</option>)}
          </select>
          <span className="small muted">{t('watch.channelHint')}</span>
        </label>
        <label className="field" style={{ flex: '0 1 150px' }}>{t('watch.after')}
          <input id="watch-minutes" type="number" min={2} max={120} value={ag.minutes} onChange={(e) => setAg({ minutes: Number(e.target.value) })} />
        </label>
        <label className="field" style={{ flex: '1 1 200px' }}>{t('watch.host')}
          <input id="watch-host" type="text" className="mono" value={ag.host} placeholder="192.168.90.114:445" onChange={(e) => setAg({ host: e.target.value })} />
          <span className="small muted">{t('watch.hostHint')}</span>
        </label>
      </div>
      {saved && <div className="alert ok">{t('conn.saved')}</div>}
      <ErrorAlert error={error} />
      <div><button className="btn" type="submit" disabled={busy}>{t('common.save')}</button></div>
    </form>
  );
}

function ChannelModal({ channel, events, onClose, onSaved }: { channel: Channel | null; events: string[]; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const [name, setName] = useState(channel?.name ?? '');
  const [type, setType] = useState<ChannelType>(channel?.type ?? 'ntfy');
  const [cfg, setCfg] = useState<Config>({});
  const [ev, setEv] = useState<string[]>(channel?.events ?? ['security', 'jobs', 'warnings']);
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [tags, setTags] = useState((channel?.tags ?? []).join(', '));
  const [tested, setTested] = useState(false);
  const { busy, error, run } = useAction();
  const body = () => ({ name, type, config: cfg, events: ev, enabled, tags: tags.split(',').map((x) => x.trim().toLowerCase()).filter(Boolean) });
  const save = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      if (channel) await put(`/api/notify/channels/${channel.id}`, body());
      else await post('/api/notify/channels', body());
      onSaved();
    });
  };
  return (
    <Modal title={channel ? t('notify.edit') : t('notify.add')} onClose={onClose} footer={
      <>
        {channel && <ConfirmButton label={t('common.delete')} onConfirm={() => run(async () => { await del(`/api/notify/channels/${channel.id}`); onSaved(); })} />}
        <span style={{ flex: 1 }} />
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button className="btn ghost" type="button" disabled={busy} onClick={() => run(async () => { setTested(false); await post('/api/notify/test', { ...body(), id: channel?.id ?? 0 }); setTested(true); })}>{t('notify.test')}</button>
        <button className="btn" type="submit" form="channel-form" disabled={busy}>{t('common.save')}</button>
      </>
    }>
      <form id="channel-form" className="stack" onSubmit={save}>
        <div className="row">
          <label className="field" style={{ flex: '2 1 180px' }}>{t('cred.name')}
            <input id="ch-name" type="text" value={name} onChange={(e) => setName(e.target.value)} required placeholder="Phone" />
          </label>
          <label className="field" style={{ flex: '1 1 140px' }}>{t('cred.type')}
            <select id="ch-type" value={type} disabled={!!channel} onChange={(e) => { setType(e.target.value as ChannelType); setCfg({}); }}>
              {(Object.keys(FIELDS) as ChannelType[]).map((x) => <option key={x} value={x}>{t('notifyType.' + x)}</option>)}
            </select>
          </label>
        </div>
        {FIELDS[type].map((f) => (
          <label className="field" key={f}>{t('notifyField.' + f)}
            <input id={'ch-' + f} type={SECRET.includes(f) ? 'password' : f === 'port' ? 'number' : 'text'} value={String(cfg[f] ?? '')}
              placeholder={channel && (SECRET.includes(f) || f === 'url') ? t('notify.unchanged') : t('notifyPh.' + type + '.' + f) !== 'notifyPh.' + type + '.' + f ? t('notifyPh.' + type + '.' + f) : ''}
              onChange={(e) => setCfg({ ...cfg, [f]: f === 'port' ? Number(e.target.value) : e.target.value })} />
          </label>
        ))}
        <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0, gap: 6 }}>
          <legend className="small" style={{ fontWeight: 500, marginBottom: 4 }}>{t('notify.events')}</legend>
          {events.map((e) => (
            <label className="check" key={e}>
              <input type="checkbox" checked={ev.includes(e)} onChange={() => setEv((cur) => cur.includes(e) ? cur.filter((x) => x !== e) : [...cur, e])} />
              <span>{t('notifyEv.' + e)}<span className="muted small"> · {t('notifyEvHint.' + e)}</span></span>
            </label>
          ))}
        </fieldset>
        {ev.includes('monitors') && (
          <label className="field">{t('notify.tags')}
            <input id="ch-tags" type="text" value={tags} placeholder="critical" onChange={(e) => setTags(e.target.value)} />
            <span className="small muted">{t('notify.tagsHint')}</span>
          </label>
        )}
        <label className="check"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />{t('notify.enabled')}</label>
        {tested && <div className="alert ok">{t('notify.testOk')}</div>}
        <ErrorAlert error={error} />
      </form>
    </Modal>
  );
}

settingsTabs.push({ path: 'notifications', labelKey: 'settings.notifications', perm: 'settings.manage', element: <NotificationsTab /> });
