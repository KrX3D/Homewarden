import { useState, type FormEvent } from 'react';
import { ApiError, post } from '../api';
import { useT } from '../i18n';
import { useSession } from '../session';
import { ErrorAlert, useAction } from '../components/ui';
import { Logo } from '../components/icons';

export default function LoginPage() {
  const t = useT();
  const { refresh } = useSession();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [needCode, setNeedCode] = useState(false);
  const { busy, error, setError, run } = useAction();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      try {
        await post('/api/auth/login', { username, password, code: needCode ? code : '' });
        await refresh();
      } catch (err) {
        if (err instanceof ApiError && err.code === 'mfa_required') {
          setNeedCode(true);
          setError(null);
          return;
        }
        throw err;
      }
    });
  };

  return (
    <div className="auth-wrap">
      <form className="card auth-card" onSubmit={submit}>
        <div className="auth-brand"><Logo /><h1>Homewarden</h1></div>
        {!needCode ? (
          <>
            <label className="field">
              {t('login.username')}
              <input id="login-username" type="text" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required autoFocus />
            </label>
            <label className="field">
              {t('login.password')}
              <input id="login-password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
            </label>
          </>
        ) : (
          <label className="field">
            {t('login.code')}
            <input id="login-code" type="text" inputMode="numeric" autoComplete="one-time-code" className="token-box" value={code} onChange={(e) => setCode(e.target.value)} maxLength={7} required autoFocus />
            <span className="hint">{t('login.codeHint')}</span>
          </label>
        )}
        <ErrorAlert error={error} />
        <button className="btn" type="submit" disabled={busy}>{t('login.submit')}</button>
        {needCode && <button type="button" className="btn subtle sm" onClick={() => { setNeedCode(false); setCode(''); }}>{t('common.back')}</button>}
      </form>
    </div>
  );
}
