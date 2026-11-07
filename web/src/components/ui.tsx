import { useEffect, useState, type ReactNode } from 'react';
import { useT, errorText } from '../i18n';

export function Modal({ title, onClose, children, footer, wide }: { title: string; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={wide ? 'modal wide' : 'modal'} role="dialog" aria-modal="true" aria-label={title}>
        <h2 className="modal-head">{title}</h2>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>
  );
}

/** A button that asks for a second click before running a destructive action. */
export function ConfirmButton({ label, confirmLabel, onConfirm, className = 'btn ghost sm', disabled }: {
  label: ReactNode; confirmLabel?: string; onConfirm: () => void | Promise<void>; className?: string; disabled?: boolean;
}) {
  const t = useT();
  const [asking, setAsking] = useState(false);
  if (!asking) return <button type="button" className={className} disabled={disabled} onClick={() => setAsking(true)}>{label}</button>;
  return (
    <span className="row" style={{ gap: 6 }}>
      <button type="button" className="btn danger sm" onClick={async () => { setAsking(false); await onConfirm(); }}>{confirmLabel ?? t('common.confirm')}</button>
      <button type="button" className="btn subtle sm" onClick={() => setAsking(false)}>{t('common.cancel')}</button>
    </span>
  );
}

export function ErrorAlert({ error }: { error: unknown }) {
  const t = useT();
  if (!error) return null;
  return <div className="alert error" role="alert">{errorText(t, error)}</div>;
}

export function Spinner() {
  return <span className="spinner" role="status" aria-label="Loading" />;
}

export function Segmented<T extends string>({ value, options, onChange, label, iconsOnly }: {
  value: T; options: { value: T; label: string; icon?: ReactNode }[]; onChange: (v: T) => void; label: string; iconsOnly?: boolean;
}) {
  return (
    <div className={'seg' + (iconsOnly ? ' icons' : '')} role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === value} onClick={() => onChange(o.value)} title={o.label} aria-label={iconsOnly ? o.label : undefined}>
          {o.icon}
          {!iconsOnly && o.label}
        </button>
      ))}
    </div>
  );
}

/** Runs an async action and tracks busy/error state for a form. */
export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, setError, run };
}
