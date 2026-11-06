import { useState, type FormEvent } from 'react';
import type { Device, PresenceConfig, Probe, ProbeMethod } from '../api';
import { useT } from '../i18n';
import { ErrorAlert, Modal } from './ui';

export type DeviceDraft = {
  name: string;
  host: string;
  groupName: string;
  parentId: number | null;
  notes: string;
  presence: PresenceConfig;
};

export const defaultPresence = (): PresenceConfig => ({
  reach: { method: 'ping' },
  ready: { method: 'none' },
  settleSeconds: 60,
  intervalSeconds: 30,
});

export function draftFrom(d?: Device | null): DeviceDraft {
  return {
    name: d?.name ?? '',
    host: d?.host ?? '',
    groupName: d?.groupName ?? '',
    parentId: d?.parentId ?? null,
    notes: d?.notes ?? '',
    presence: d?.presence ?? defaultPresence(),
  };
}

function ProbeFields({ id, value, onChange, allowNone, noneLabel }: {
  id: string; value: Probe; onChange: (p: Probe) => void; allowNone: boolean; noneLabel: string;
}) {
  const t = useT();
  return (
    <div className="row" style={{ alignItems: 'flex-end' }}>
      <label className="field" style={{ flex: '1 1 160px' }}>
        {t('deviceForm.method')}
        <select id={id + '-method'} value={value.method} onChange={(e) => onChange({ ...value, method: e.target.value as ProbeMethod })}>
          {allowNone && <option value="none">{noneLabel}</option>}
          <option value="ping">{t('probe.ping')}</option>
          <option value="tcp">{t('probe.tcp')}</option>
          <option value="http">{t('probe.http')}</option>
        </select>
      </label>
      {value.method === 'tcp' && (
        <label className="field" style={{ flex: '0 1 110px' }}>
          {t('deviceForm.port')}
          <input id={id + '-port'} type="number" min={1} max={65535} value={value.port ?? ''} onChange={(e) => onChange({ ...value, port: Number(e.target.value) })} required />
        </label>
      )}
      {value.method === 'http' && (
        <label className="field" style={{ flex: '2 1 220px' }}>
          {t('deviceForm.url')}
          <input id={id + '-url'} type="url" placeholder="http://192.168.1.10/" value={value.url ?? ''} onChange={(e) => onChange({ ...value, url: e.target.value })} />
        </label>
      )}
    </div>
  );
}

export function DeviceFormModal({ title, initial, parents, onClose, onSubmit, busy, error }: {
  title: string; initial: DeviceDraft; parents: Device[]; onClose: () => void;
  onSubmit: (d: DeviceDraft) => void; busy: boolean; error: unknown;
}) {
  const t = useT();
  const [d, setD] = useState<DeviceDraft>(initial);
  const set = <K extends keyof DeviceDraft>(k: K, v: DeviceDraft[K]) => setD((cur) => ({ ...cur, [k]: v }));
  const setP = (p: Partial<PresenceConfig>) => setD((cur) => ({ ...cur, presence: { ...cur.presence, ...p } }));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSubmit(d);
  };
  return (
    <Modal title={title} onClose={onClose} footer={
      <>
        <button className="btn subtle" type="button" onClick={onClose}>{t('common.cancel')}</button>
        <button className="btn" type="submit" form="device-form" disabled={busy}>{t('common.save')}</button>
      </>
    }>
      <form id="device-form" className="stack" onSubmit={submit}>
        <div className="grid-2" style={{ gap: 12 }}>
          <label className="field">
            {t('deviceForm.name')}
            <input id="dev-name" type="text" value={d.name} onChange={(e) => set('name', e.target.value)} required maxLength={128} placeholder="OctoPi Left" />
          </label>
          <label className="field">
            {t('deviceForm.host')}
            <input id="dev-host" type="text" value={d.host} onChange={(e) => set('host', e.target.value)} placeholder="192.168.20.117" />
          </label>
          <label className="field">
            {t('deviceForm.group')}
            <input id="dev-group" type="text" value={d.groupName} onChange={(e) => set('groupName', e.target.value)} placeholder="Raspberry Pis" list="dev-groups" />
            <datalist id="dev-groups">
              {[...new Set(parents.map((p) => p.groupName).filter(Boolean))].map((g) => <option key={g} value={g} />)}
            </datalist>
          </label>
          <label className="field">
            {t('deviceForm.parent')}
            <select id="dev-parent" value={d.parentId ?? ''} onChange={(e) => set('parentId', e.target.value ? Number(e.target.value) : null)}>
              <option value="">{t('deviceForm.noParent')}</option>
              {parents.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          </label>
        </div>

        <fieldset className="stack" style={{ border: '1px solid var(--line)', borderRadius: 8, padding: 12, margin: 0 }}>
          <legend className="small" style={{ fontWeight: 600, padding: '0 4px' }}>{t('deviceForm.presence')}</legend>
          <p className="small muted">{t('deviceForm.presenceIntro')}</p>
          <div className="small" style={{ fontWeight: 500 }}>{t('deviceForm.reach')}</div>
          <ProbeFields id="reach" value={d.presence.reach} onChange={(reach) => setP({ reach })} allowNone noneLabel={t('probe.noneReach')} />
          <div className="small" style={{ fontWeight: 500 }}>{t('deviceForm.ready')}</div>
          <p className="small muted">{t('deviceForm.readyHint')}</p>
          <ProbeFields id="ready" value={d.presence.ready} onChange={(ready) => setP({ ready })} allowNone noneLabel={t('probe.noneReady')} />
          <div className="row">
            <label className="field" style={{ flex: '1 1 140px' }}>
              {t('deviceForm.settle')}
              <input id="dev-settle" type="number" min={0} max={3600} value={d.presence.settleSeconds} onChange={(e) => setP({ settleSeconds: Number(e.target.value) })} />
              <span className="hint">{t('deviceForm.settleHint')}</span>
            </label>
            <label className="field" style={{ flex: '1 1 140px' }}>
              {t('deviceForm.interval')}
              <input id="dev-interval" type="number" min={10} max={3600} value={d.presence.intervalSeconds} onChange={(e) => setP({ intervalSeconds: Number(e.target.value) })} />
              <span className="hint">{t('deviceForm.intervalHint')}</span>
            </label>
          </div>
        </fieldset>

        <label className="field">
          {t('deviceForm.notes')}
          <textarea id="dev-notes" rows={2} value={d.notes} onChange={(e) => set('notes', e.target.value)} style={{ fontFamily: 'var(--body)', fontSize: 14 }} />
        </label>
        <ErrorAlert error={error} />
      </form>
    </Modal>
  );
}
