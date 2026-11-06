import type { Device, DeviceState } from '../api';
import { useT } from '../i18n';

const stateClass: Record<DeviceState, string> = {
  ready: 'ok',
  reachable: 'warn',
  offline: 'off',
  unknown: 'off',
  paused: 'off',
};

export function StatePill({ state }: { state: DeviceState }) {
  const t = useT();
  return <span className={'pill dot ' + (stateClass[state] ?? 'off')} title={t('stateHint.' + state)}>{t('state.' + state)}</span>;
}

/** Devices whose presence comes from somewhere else (e.g. a container) show no check. */
export function hasPresence(d: Device) {
  return d.presence.reach.method !== 'none' && !!d.host;
}
