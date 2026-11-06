import { useState, type FormEvent } from 'react';
import { post } from '../api';
import { useT } from '../i18n';
import { useSession } from '../session';
import { ErrorAlert, useAction } from '../components/ui';
import { Logo } from '../components/icons';

export default function SetupPage() {
  const t = useT();
  const { meta, refresh } = useSession();
  const [token, setToken] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [repeat, setRepeat] = useState('');
  const { busy, error, setError, run } = useAction();
  const min = meta?.minPassword ?? 10;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (password !== repeat) {
      setError(new Error('mismatch'));
      return;
    }
    run(async () => {
      await post('/api/setup', { token, username, password });
      await refresh();
    });
  };

  return (
    <div className="auth-wrap">
      <form className="card auth-card" onSubmit={submit}>
        <div className="auth-brand"><Logo /><h1>Homewarden</h1></div>
        <div className="stack">
          <h2>{t('setup.title')}</h2>
          <p className="muted small">{t('setup.intro')}</p>
        </div>
        <label className="field">
          {t('setup.token')}
          <input id="setup-token" type="text" className="token-box" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder="XXXX-XXXX-XXXX-XXXX" required />
          <span className="hint">{t('setup.tokenHint')}</span>
        </label>
        <label className="field">
          {t('setup.username')}
          <input id="setup-username" type="text" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
          <span className="hint">{t('setup.usernameHint')}</span>
        </label>
        <label className="field">
          {t('setup.password')}
          <input id="setup-password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={min} required />
          <span className="hint">{t('common.passwordHint', { n: min })}</span>
        </label>
        <label className="field">
          {t('setup.repeat')}
          <input id="setup-repeat" type="password" autoComplete="new-password" value={repeat} onChange={(e) => setRepeat(e.target.value)} required />
        </label>
        {error instanceof Error && error.message === 'mismatch' ? <div className="alert error">{t('error.password_mismatch')}</div> : <ErrorAlert error={error} />}
        <button className="btn" type="submit" disabled={busy}>{t('setup.submit')}</button>
      </form>
    </div>
  );
}
