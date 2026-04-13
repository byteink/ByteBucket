import { FormEvent, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { saveSession } from '../lib/session';
import { checkAdminAuth } from '../lib/admin';

// LoginPage collects admin credentials. There is no separate storage endpoint
// any more: the UI and the storage API are same-origin on the admin port.
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
      navigate('/buckets', { replace: true });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="min-h-full flex items-center justify-center px-6 py-16">
      <form onSubmit={onSubmit} className="w-full max-w-sm">
        <h1 className="text-lg mb-8 font-mono">ByteBucket Admin</h1>
        <div className="space-y-4">
          <div>
            <label className="field-label" htmlFor="ak">Access key</label>
            <input
              id="ak"
              className="input font-mono"
              autoComplete="username"
              value={accessKey}
              onChange={(e) => setAccessKey(e.target.value)}
              required
            />
          </div>
          <div>
            <label className="field-label" htmlFor="sk">Secret</label>
            <input
              id="sk"
              className="input font-mono"
              type="password"
              autoComplete="current-password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              required
            />
          </div>
          {error && (
            <div role="alert" className="text-xs text-ink-900 border-l-2 border-ink-900 pl-3">
              {error}
            </div>
          )}
          <button type="submit" disabled={busy} className="btn-primary w-full">
            {busy ? 'Signing in' : 'Sign in'}
          </button>
        </div>
      </form>
    </div>
  );
}
