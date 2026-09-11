import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { saveSession } from '../lib/session';
import { checkAdminAuth } from '../lib/admin';
import ThemeToggle from '../components/ThemeToggle';
import { ErrorBanner } from '../components/ErrorBanner';

// LoginPage collects admin credentials. The UI and the storage API are
// same-origin on the admin port, so there is no separate endpoint to ask for.
export default function LoginPage() {
  const navigate = useNavigate();
  const [accessKey, setAccessKey] = useState('');
  const [secret, setSecret] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setError(null);
    setBusy(true);
    try {
      const session = { accessKey, secret };
      const adminErr = await checkAdminAuth(session);
      if (adminErr) {
        setError(adminErr);
        return;
      }
      saveSession(session);
      navigate('/dashboard', { replace: true });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="relative min-h-full flex items-center justify-center px-6 py-16">
      <div className="absolute top-3 right-3">
        <ThemeToggle />
      </div>
      <form onSubmit={onSubmit} className="w-full max-w-[360px] flex flex-col gap-4">
        <div className="mb-3">
          <h1 className="font-mono text-lg font-normal">ByteBucket</h1>
          <p className="text-xs text-ink-500 mt-1">Admin console · sign in with an admin access key</p>
        </div>
        <div>
          <label className="field-label" htmlFor="ak">
            Access key ID
          </label>
          <input
            id="ak"
            className="input-mono"
            autoComplete="username"
            value={accessKey}
            onChange={(e) => setAccessKey(e.target.value)}
            required
          />
        </div>
        <div>
          <label className="field-label" htmlFor="sk">
            Secret access key
          </label>
          <input
            id="sk"
            className="input-mono"
            type="password"
            autoComplete="current-password"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            required
          />
        </div>
        {error && <ErrorBanner message={error} />}
        <button type="submit" disabled={busy} className="btn-primary w-full">
          {busy ? 'Signing in' : 'Sign in'}
        </button>
      </form>
    </div>
  );
}
