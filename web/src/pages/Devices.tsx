import { useT } from '../i18n';
import { IconDevices } from '../components/icons';

export default function DevicesPage() {
  const t = useT();
  return (
    <>
      <div className="page-head"><h1>{t('devices.title')}</h1></div>
      <div className="card empty">
        <IconDevices />
        <h2>{t('devices.emptyTitle')}</h2>
        <p>{t('devices.emptySoon')}</p>
      </div>
    </>
  );
}
