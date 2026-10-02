import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { login } from '../lib/session';
import ThemeToggle from '../components/ThemeToggle';
import { ErrorBanner } from '../components/ErrorBanner';

// LoginPage posts the admin credentials once to /api/login. The server answers
// with an HttpOnly session cookie, so the secret leaves the form state and is
// never persisted by the browser.
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
      await login(accessKey, secret);
      setSecret('');
      navigate('/dashboard', { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
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
