import { useState } from 'react';
import { loadSession, saveSession } from '../lib/session';

export default function SettingsPage() {
  const session = loadSession();
  const [endpoint, setEndpoint] = useState(session?.storageEndpoint ?? '');
  const [status, setStatus] = useState<string | null>(null);

  function onSave() {
    if (!session) return;
    const trimmed = endpoint.trim();
    if (!trimmed) {
      setStatus('Endpoint cannot be empty.');
      return;
    }
    saveSession({ ...session, storageEndpoint: trimmed });
    setStatus('Saved.');
  }

  return (
    <section className="max-w-xl">
      <h2 className="text-base mb-6">Settings</h2>
      <div>
        <label className="field-label" htmlFor="ep">S3 endpoint</label>
        <input
          id="ep"
          className="input font-mono"
          value={endpoint}
          onChange={(e) => setEndpoint(e.target.value)}
        />
        <p className="text-xs text-ink-500 mt-2">
          Used by the browser to sign S3 requests. Must be reachable from your browser.
        </p>
      </div>
      {status && <div className="text-xs text-ink-500 mt-4">{status}</div>}
      <div className="flex justify-end mt-6">
        <button className="btn-primary" onClick={onSave}>Save</button>
      </div>
    </section>
  );
}
