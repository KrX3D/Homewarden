// Notification channels and settings (Settings → Notifications).
import { useEffect, useState, type FormEvent } from 'react';
import { del, get, post, put } from '../api';
import { useT } from '../i18n';
import { ConfirmButton, ErrorAlert, Modal, useAction } from '../components/ui';
import { IconEdit, IconPlus } from '../components/icons';
import { settingsTabs } from '../extensions';

type ChannelType = 'discord' | 'ntfy' | 'telegram' | 'gotify' | 'email' | 'webhook';
type Config = { url?: string; topic?: string; token?: string; chatId?: string; host?: string; port?: number; username?: string; password?: string; from?: string; to?: string };
type Channel = { id: number; name: string; type: ChannelType; events: string[]; enabled: boolean; summary: string };
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
              <div className="row" style={{ gap: 6 }}>{c.events.map((e) => <span key={e} className="chip">{t('notifyEv.' + e)}</span>)}</div>
            </div>
          ))}
        </div>
      )}
      {editing && <ChannelModal channel={editing === 'new' ? null : editing} events={data.events} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />}
    </section>
  );
}

function ChannelModal({ channel, events, onClose, onSaved }: { channel: Channel | null; events: string[]; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const [name, setName] = useState(channel?.name ?? '');
  const [type, setType] = useState<ChannelType>(channel?.type ?? 'ntfy');
  const [cfg, setCfg] = useState<Config>({});
  const [ev, setEv] = useState<string[]>(channel?.events ?? ['security', 'jobs', 'warnings']);
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [tested, setTested] = useState(false);
  const { busy, error, run } = useAction();
  const body = () => ({ name, type, config: cfg, events: ev, enabled });
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
        <label className="check"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />{t('notify.enabled')}</label>
        {tested && <div className="alert ok">{t('notify.testOk')}</div>}
        <ErrorAlert error={error} />
      </form>
    </Modal>
  );
}

settingsTabs.push({ path: 'notifications', labelKey: 'settings.notifications', perm: 'settings.manage', element: <NotificationsTab /> });
