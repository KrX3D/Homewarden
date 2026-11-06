// Backups: targets, the overview with backup age, the device Backup card and the
// device busy-guard rules.
import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import {
  del, get, post, put, type Backup, type BackupSettings, type BackupTarget, type Credential, type Device, type Guard, type Integration,
} from '../api';
import { formatAgo, formatTime, useLang, useT } from '../i18n';
import { useSession } from '../session';
import { useEvents } from '../useEvents';
import { ConfirmButton, ErrorAlert, Modal, Spinner, useAction } from '../components/ui';
import { IconBackup, IconEdit, IconPlus, IconTrash } from '../components/icons';
import { deviceSections, navItems } from '../extensions';

const MAX_AGE_DAYS = 14;
const mb = (n: number) => (n < 1048576 ? `${Math.max(1, Math.round(n / 1024))} KB` : `${(n / 1048576).toFixed(1)} MB`);

function useBackupData() {
  const [targets, setTargets] = useState<BackupTarget[]>([]);
  const [backups, setBackups] = useState<Backup[]>([]);
  const load = useCallback(() => {
    get<BackupTarget[]>('/api/backup-targets').then(setTargets).catch(() => {});
    get<Backup[]>('/api/backups').then(setBackups).catch(() => {});
  }, []);
  useEffect(() => { load(); }, [load]);
  useEvents({ 'device.backup': load }, load);
  return { targets, backups, reload: load };
}

function AgePill({ at }: { at: number | undefined }) {
  const t = useT();
  const lang = useLang();
  if (!at) return <span className="pill off">{t('backups.never')}</span>;
  const days = (Date.now() / 1000 - at) / 86400;
  return <span className={'pill dot ' + (days > MAX_AGE_DAYS ? 'warn' : 'ok')}>{formatAgo(lang, at)}</span>;
}

// ---------------------------------------------------------------- page

function BackupsPage() {
  const t = useT();
  const { can } = useSession();
  const { targets, backups, reload } = useBackupData();
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [integrations, setIntegrations] = useState<Integration[]>([]);
  const [editing, setEditing] = useState<BackupTarget | 'new' | null>(null);
  const run = useAction();
  useEffect(() => {
    get<Device[]>('/api/devices?status=active').then(setDevices).catch(() => {});
    get<Integration[]>('/api/integrations').then(setIntegrations).catch(() => {});
  }, []);
  const capable = (devices ?? []).filter((d) => integrations.find((i) => i.id === d.kind)?.capabilities.includes('backup'));
  const last = (id: number) => backups.find((b) => b.deviceId === id);
  const tname = (id: number | null) => targets.find((x) => x.id === id)?.name ?? '–';
  const stale = capable.filter((d) => { const l = last(d.id); return !l || Date.now() / 1000 - l.at > MAX_AGE_DAYS * 86400; }).length;

  return (
    <>
      <div className="page-head">
        <h1 className="grow">{t('backups.title')}</h1>
        {stale > 0 && <span className="pill warn">{t('backups.stale', { n: stale, d: MAX_AGE_DAYS })}</span>}
      </div>
      <ErrorAlert error={run.error} />

      <section className="stack">
        <h2>{t('backups.devices')}</h2>
        {!devices ? <Spinner /> : capable.length === 0 ? (
          <div className="card empty"><IconBackup /><h2>{t('backups.noDevicesTitle')}</h2><p>{t('backups.noDevicesText')}</p></div>
        ) : (
          <div className="table-wrap">
            <table className="tbl">
              <thead><tr><th>{t('devices.colName')}</th><th>{t('backups.target')}</th><th>{t('backups.last')}</th><th>{t('backups.size')}</th><th></th></tr></thead>
              <tbody>
                {capable.map((d) => {
                  const l = last(d.id);
                  const bs = (d.config as { backup?: BackupSettings }).backup;
                  return (
                    <tr key={d.id}>
                      <td><Link to={`/devices/${d.id}`}><strong>{d.name}</strong></Link></td>
                      <td>{bs?.targetId ? tname(bs.targetId) : <span className="muted">{t('backups.noTarget')}</span>}</td>
                      <td><AgePill at={l?.at} /></td>
                      <td className="small mono">{l ? mb(l.size) : ''}</td>
                      <td style={{ textAlign: 'right' }}>
                        {can('backups.run') && bs?.targetId ? <button className="btn ghost sm" type="button" onClick={() => run.run(async () => { await post(`/api/devices/${d.id}/backup`); })}>{t('backups.runNow')}</button> : null}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="stack">
        <div className="page-head">
          <h2 className="grow">{t('backups.targets')}</h2>
          {can('backups.manage') && <button className="btn" type="button" onClick={() => setEditing('new')}><IconPlus />{t('backups.addTarget')}</button>}
        </div>
        {targets.length === 0 ? <p className="muted">{t('backups.noTargets')}</p> : (
          <div className="grid-2">
            {targets.map((x) => (
              <div className="card" key={x.id}>
                <div className="card-head">
                  <h3 className="grow">{x.name}</h3>
                  <span className="chip">{x.type === 'smb' ? 'SMB' : t('backups.typeLocal')}</span>
                  {can('backups.manage') && <button className="btn subtle sm" type="button" aria-label={t('common.edit')} onClick={() => setEditing(x)}><IconEdit /></button>}
                </div>
                <span className="mono small muted">{x.type === 'smb' ? `\\\\${x.config.host}\\${x.config.share}${x.config.folder ? '\\' + x.config.folder : ''}` : x.config.folder}</span>
              </div>
            ))}
          </div>
        )}
      </section>
      {editing && <TargetModal target={editing === 'new' ? null : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); reload(); }} />}
    </>
  );
}

function TargetModal({ target, onClose, onSaved }: { target: BackupTarget | null; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const [name, setName] = useState(target?.name ?? '');
  const [type, setType] = useState<'smb' | 'local'>(target?.type ?? 'smb');
  const [cfg, setCfg] = useState(target?.config ?? { folder: '' });
  const [creds, setCreds] = useState<Credential[]>([]);
  const [tested, setTested] = useState(false);
  const { busy, error, run } = useAction();
  useEffect(() => { get<Credential[]>('/api/credentials').then(setCreds).catch(() => {}); }, []);
  const body = () => ({ name, type, config: cfg });
  const save = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      if (target) await put(`/api/backup-targets/${target.id}`, body());
      else await post('/api/backup-targets', body());
      onSaved();
    });
  };
  const set = (k: string, v: unknown) => { setTested(false); setCfg((c) => ({ ...c, [k]: v })); };
  return (
    <Modal title={target ? t('backups.editTarget') : t('backups.addTarget')} onClose={onClose} footer={
      <>
        {target && <ConfirmButton label={t('common.delete')} onConfirm={() => run(async () => { await del(`/api/backup-targets/${target.id}`); onSaved(); })} />}
        <span style={{ flex: 1 }} />
        <button className="btn ghost" type="button" disabled={busy} onClick={() => run(async () => { setTested(false); await post('/api/backup-targets/test', body()); setTested(true); })}>{t('backups.test')}</button>
        <button className="btn" type="submit" form="target-form" disabled={busy}>{t('common.save')}</button>
      </>
    }>
      <form id="target-form" className="stack" onSubmit={save}>
        <label className="field">{t('cred.name')}
          <input id="tgt-name" type="text" value={name} onChange={(e) => setName(e.target.value)} required placeholder="NAS backups" />
        </label>
        <label className="field">{t('cred.type')}
          <select id="tgt-type" value={type} onChange={(e) => { setTested(false); setType(e.target.value as 'smb' | 'local'); }}>
            <option value="smb">{t('backups.typeSmb')}</option>
            <option value="local">{t('backups.typeLocalLong')}</option>
          </select>
        </label>
        {type === 'smb' ? (
          <>
            <div className="row">
              <label className="field" style={{ flex: '2 1 180px' }}>{t('backups.host')}
                <input id="tgt-host" type="text" value={cfg.host ?? ''} onChange={(e) => set('host', e.target.value)} placeholder="192.168.90.114" required />
              </label>
              <label className="field" style={{ flex: '1 1 140px' }}>{t('backups.share')}
                <input id="tgt-share" type="text" value={cfg.share ?? ''} onChange={(e) => set('share', e.target.value)} placeholder="Backups" required />
              </label>
            </div>
            <label className="field">{t('backups.folder')}
              <input id="tgt-folder" type="text" value={cfg.folder ?? ''} onChange={(e) => set('folder', e.target.value)} placeholder="homewarden" />
              <span className="hint">{t('backups.folderHint')}</span>
            </label>
            <label className="field">{t('backups.login')}
              <select id="tgt-cred" value={cfg.credentialId ?? ''} onChange={(e) => set('credentialId', e.target.value ? Number(e.target.value) : undefined)}>
                <option value="">{t('backups.guest')}</option>
                {creds.filter((c) => c.type === 'password').map((c) => <option key={c.id} value={c.id}>{c.name} ({c.username})</option>)}
              </select>
              <span className="hint">{t('backups.loginHint')}</span>
            </label>
          </>
        ) : (
          <label className="field">{t('backups.path')}
            <input id="tgt-path" type="text" className="mono" value={cfg.folder ?? ''} onChange={(e) => set('folder', e.target.value)} placeholder="/backups" required />
            <span className="hint">{t('backups.pathHint')}</span>
          </label>
        )}
        {tested && <div className="alert ok">{t('backups.testOk')}</div>}
        <ErrorAlert error={error} />
      </form>
    </Modal>
  );
}

// ---------------------------------------------------------------- device cards

function BackupCard({ device }: { device: Device }) {
  const t = useT();
  const lang = useLang();
  const { can } = useSession();
  const [integ, setInteg] = useState<Integration | null>(null);
  const [targets, setTargets] = useState<BackupTarget[]>([]);
  const [list, setList] = useState<Backup[]>([]);
  const saved: BackupSettings = { targetId: 0, beforeUpdate: false, keep: 5, ...((device.config as { backup?: BackupSettings }).backup ?? {}) };
  const [bs, setBs] = useState<BackupSettings>(saved);
  const [ok, setOk] = useState(false);
  const [restoring, setRestoring] = useState<Backup | null>(null);
  const { busy, error, run } = useAction();
  const load = useCallback(() => { get<Backup[]>(`/api/backups?device=${device.id}`).then(setList).catch(() => {}); }, [device.id]);
  useEffect(() => {
    get<Integration[]>('/api/integrations').then((l) => setInteg(l.find((i) => i.id === device.kind) ?? null)).catch(() => {});
    get<BackupTarget[]>('/api/backup-targets').then(setTargets).catch(() => {});
    load();
  }, [device.kind, load]);
  useEffect(() => setBs(saved), [JSON.stringify(saved)]); // eslint-disable-line react-hooks/exhaustive-deps
  useEvents({ 'device.backup': (e: { id: number }) => e.id === device.id && load() });
  if (!integ?.capabilities.includes('backup')) return null;
  const manage = can('backups.manage');

  return (
    <section className="card">
      <div className="card-head">
        <h2 className="grow">{t('backups.card')}</h2>
        <AgePill at={list[0]?.at} />
        {can('backups.run') && saved.targetId > 0 && <button className="btn ghost sm" type="button" disabled={busy} onClick={() => run(async () => { await post(`/api/devices/${device.id}/backup`); })}>{t('backups.runNow')}</button>}
      </div>
      {targets.length === 0 ? (
        <p className="small muted">{t('backups.needTarget')} <Link to="/backups">{t('backups.title')}</Link></p>
      ) : manage && (
        <form className="stack" onSubmit={(e) => { e.preventDefault(); setOk(false); run(async () => { await put(`/api/devices/${device.id}/backup-settings`, bs); setOk(true); }); }}>
          <div className="row" style={{ alignItems: 'flex-end' }}>
            <label className="field" style={{ flex: '2 1 200px' }}>{t('backups.target')}
              <select id="bk-target" value={bs.targetId || ''} onChange={(e) => setBs({ ...bs, targetId: Number(e.target.value) || 0 })}>
                <option value="">{t('backups.noTarget')}</option>
                {targets.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
              </select>
            </label>
            <label className="field" style={{ flex: '0 1 110px' }}>{t('backups.keep')}
              <input id="bk-keep" type="number" min={1} max={100} value={bs.keep} onChange={(e) => setBs({ ...bs, keep: Number(e.target.value) })} />
            </label>
          </div>
          <label className="field">{t('auto.backupSchedule')}
            <input id="bk-schedule" type="text" className="mono" value={bs.schedule ?? ''} onChange={(e) => setBs({ ...bs, schedule: e.target.value })} placeholder="0 2 * * *" />
            <span className="hint">{t('auto.cronHint')}</span>
          </label>
          <label className="check"><input id="bk-before" type="checkbox" checked={bs.beforeUpdate} onChange={(e) => setBs({ ...bs, beforeUpdate: e.target.checked })} />{t('backups.beforeUpdate')}</label>
          {ok && <div className="alert ok">{t('conn.saved')}</div>}
          <div><button className="btn" type="submit" disabled={busy}>{t('common.save')}</button></div>
        </form>
      )}
      <ErrorAlert error={error} />
      {list.length > 0 && (
        <div className="table-wrap">
          <table className="tbl"><tbody>
            {list.map((b) => (
              <tr key={b.id}>
                <td className="small">{b.name}</td>
                <td className="small muted">{formatTime(lang, b.at)}</td>
                <td className="small mono">{mb(b.size)}</td>
                <td style={{ textAlign: 'right' }}>{can('backups.run') && <span className="row" style={{ gap: 10, justifyContent: 'flex-end' }}>
                  <a className="small" href={`/api/backups/${b.id}/download`}>{t('backups.download')}</a>
                  {integ.capabilities.includes('restore') && <button type="button" className="btn subtle sm" onClick={() => setRestoring(b)}>{t('restore.button')}</button>}
                </span>}</td>
              </tr>
            ))}
          </tbody></table>
        </div>
      )}
      {restoring && <RestoreModal device={device} backup={restoring} onClose={() => setRestoring(null)} />}
    </section>
  );
}

function RestoreModal({ device, backup, onClose }: { device: Device; backup: Backup; onClose: () => void }) {
  const t = useT();
  const lang = useLang();
  const [targets, setTargets] = useState<Device[]>([]);
  const [target, setTarget] = useState(device.id);
  const [jobId, setJobId] = useState(0);
  const { busy, error, run } = useAction();
  useEffect(() => { get<Device[]>('/api/devices?status=active').then((l) => setTargets(l.filter((d) => d.kind === device.kind))).catch(() => {}); }, [device.kind]);
  return (
    <Modal title={t('restore.title')} onClose={onClose} footer={jobId ? <Link className="btn" to={`/jobs/${jobId}`}>{t('sw.showLog')}</Link> : (
      <>
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button className="btn danger" type="button" disabled={busy} onClick={() => run(async () => { const j = await post<{ id: number }>(`/api/backups/${backup.id}/restore`, { deviceId: target }); setJobId(j.id); })}>{t('restore.start')}</button>
      </>
    )}>
      <p className="small">{t('restore.intro', { name: backup.name, when: formatTime(lang, backup.at) })}</p>
      <label className="field">{t('restore.onto')}
        <select id="restore-target" value={target} onChange={(e) => setTarget(Number(e.target.value))}>
          {targets.map((d) => <option key={d.id} value={d.id}>{d.name}{d.id === device.id ? ' ' + t('restore.same') : ''}</option>)}
        </select>
        <span className="hint">{t('restore.ontoHint')}</span>
      </label>
      <div className="alert warn small">{t('restore.warning')}</div>
      {jobId > 0 && <div className="alert ok">{t('restore.started')}</div>}
      <ErrorAlert error={error} />
    </Modal>
  );
}

function GuardsCard({ device }: { device: Device }) {
  const t = useT();
  const { can } = useSession();
  const saved = ((device.config as { guards?: Guard[] }).guards ?? []);
  const [guards, setGuards] = useState<Guard[]>(saved);
  const [ok, setOk] = useState(false);
  const { busy, error, run } = useAction();
  useEffect(() => setGuards(saved), [JSON.stringify(saved)]); // eslint-disable-line react-hooks/exhaustive-deps
  if (device.kind === 'generic') return null;
  const manage = can('devices.manage');
  const set = (i: number, g: Partial<Guard>) => { setOk(false); setGuards((cur) => cur.map((x, j) => (j === i ? { ...x, ...g } : x))); };

  return (
    <section className="card">
      <h2>{t('guards.title')}</h2>
      <p className="small muted">{t('guards.intro')}</p>
      {guards.map((g, i) => (
        <div key={i} className="stack guard-row">
          <div className="row" style={{ alignItems: 'flex-end' }}>
            <label className="field" style={{ flex: '2 1 160px' }}>{t('guards.name')}
              <input type="text" value={g.name} disabled={!manage} onChange={(e) => set(i, { name: e.target.value })} placeholder={t('guards.namePh')} />
            </label>
            <label className="field" style={{ flex: '1 1 150px' }}>{t('guards.type')}
              <select value={g.type} disabled={!manage} onChange={(e) => set(i, { type: e.target.value as Guard['type'] })}>
                <option value="ssh">{t('guards.ssh')}</option>
                <option value="http">{t('guards.http')}</option>
              </select>
            </label>
            <label className="field" style={{ flex: '1 1 130px' }}>{t('guards.mode')}
              <select value={g.mode} disabled={!manage} onChange={(e) => set(i, { mode: e.target.value as Guard['mode'] })}>
                <option value="block">{t('guards.block')}</option>
                <option value="warn">{t('guards.warn')}</option>
              </select>
            </label>
            {manage && <button className="btn subtle" type="button" aria-label={t('common.delete')} onClick={() => setGuards((cur) => cur.filter((_, j) => j !== i))}><IconTrash /></button>}
          </div>
          {g.type === 'ssh' ? (
            <label className="field">{t('guards.command')}
              <input type="text" className="mono" value={g.command ?? ''} disabled={!manage} onChange={(e) => set(i, { command: e.target.value })} placeholder="pgrep -x rsync" />
              <span className="hint">{t('guards.commandHint')}</span>
            </label>
          ) : (
            <div className="row">
              <label className="field" style={{ flex: '2 1 220px' }}>URL
                <input type="url" value={g.url ?? ''} disabled={!manage} onChange={(e) => set(i, { url: e.target.value })} placeholder="http://192.168.90.114:32400/status/sessions" />
              </label>
              <label className="field" style={{ flex: '1 1 140px' }}>{t('guards.contains')}
                <input type="text" value={g.contains ?? ''} disabled={!manage} onChange={(e) => set(i, { contains: e.target.value })} placeholder='size="1"' />
              </label>
            </div>
          )}
        </div>
      ))}
      {manage && (
        <div className="row">
          <button className="btn subtle" type="button" onClick={() => setGuards((cur) => [...cur, { name: '', type: 'ssh', command: '', mode: 'block' }])}><IconPlus />{t('guards.add')}</button>
          <button className="btn" type="button" disabled={busy} onClick={() => run(async () => { await put(`/api/devices/${device.id}/guards`, guards); setOk(true); })}>{t('common.save')}</button>
        </div>
      )}
      {ok && <div className="alert ok">{t('conn.saved')}</div>}
      <ErrorAlert error={error} />
    </section>
  );
}

navItems.splice(1, 0, { path: 'backups', labelKey: 'nav.backups', perm: 'devices.view', icon: <IconBackup />, element: <BackupsPage /> });
// After Software and Integration, before Connection.
deviceSections.splice(2, 0, BackupCard, GuardsCard);
