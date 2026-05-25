import { useEffect, useState } from 'react';
import {
  deleteRateLimit,
  getRateLimit,
  putRateLimit,
  type RateLimitConfig,
  type RateLimitState,
} from '../lib/admin';
import { loadSession } from '../lib/session';
import { ErrorBanner } from '../components/ErrorBanner';

export default function SettingsPage() {
  const session = loadSession();
  const [state, setState] = useState<RateLimitState | null>(null);
  const [form, setForm] = useState<RateLimitConfig | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!session) return;
    getRateLimit(session)
      .then((s) => {
        setState(s);
        setForm(s.effective);
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
    // session is read once from localStorage; refetching on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function patch(p: Partial<RateLimitConfig>) {
    setForm((f) => (f ? { ...f, ...p } : f));
  }

  async function onSave() {
    if (!session || !form) return;
    setError(null);
    setNotice(null);
    setBusy(true);
    try {
      const effective = await putRateLimit(session, form);
      setState({ env: state?.env ?? effective, override: form, effective });
      setForm(effective);
      setNotice('Override saved and applied.');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function onReset() {
    if (!session) return;
    setError(null);
    setNotice(null);
    setBusy(true);
    try {
      const effective = await deleteRateLimit(session);
      setState({ env: state?.env ?? effective, override: null, effective });
      setForm(effective);
      setNotice('Reverted to environment defaults.');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h2 className="text-base mb-1">Settings</h2>
      <p className="text-xs text-ink-500 mb-6">
        Runtime configuration. A saved override takes effect immediately on both ports and persists
        across restarts, replacing the environment defaults.
      </p>

      {error && <ErrorBanner message={error} className="mb-4" />}
      {notice && <div className="text-xs text-ink-500 border-l-2 border-ink-300 pl-3 mb-4">{notice}</div>}

      <div className="mb-2 flex items-baseline justify-between">
        <h3 className="text-sm">Rate limiting</h3>
        <span className="text-xs text-ink-500">
          Source: {state?.override ? 'runtime override' : 'environment defaults'}
        </span>
      </div>

      {!form ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <div className="border border-ink-200 p-4 max-w-md space-y-4">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={form.enabled}
              onChange={(e) => patch({ enabled: e.target.checked })}
            />
            Enable request rate limiting
          </label>

          <NumberField
            label="Requests per second"
            value={form.rps}
            step="0.1"
            min={0}
            onChange={(v) => patch({ rps: v })}
            hint={`Default: ${fmt(state?.env.rps)}`}
          />
          <NumberField
            label="Burst"
            value={form.burst}
            step="1"
            min={0}
            onChange={(v) => patch({ burst: Math.trunc(v) })}
            hint={`Default: ${fmt(state?.env.burst)}`}
          />
          <NumberField
            label="Trusted proxies"
            value={form.trustedProxies}
            step="1"
            min={0}
            onChange={(v) => patch({ trustedProxies: Math.trunc(v) })}
            hint={`Reverse-proxy hops in front of the server. Default: ${fmt(state?.env.trustedProxies)}`}
          />

          <div className="flex gap-2 pt-1">
            <button className="btn-primary h-8 px-3 text-xs" disabled={busy} onClick={onSave}>
              {busy ? 'Saving' : 'Save override'}
            </button>
            <button
              className="btn h-8 px-3 text-xs"
              disabled={busy || !state?.override}
              onClick={onReset}
              title={state?.override ? '' : 'No override set'}
            >
              Reset to defaults
            </button>
          </div>
        </div>
      )}
    </section>
  );
}

function fmt(n: number | undefined): string {
  return n === undefined ? '-' : String(n);
}

function NumberField({
  label,
  value,
  step,
  min,
  onChange,
  hint,
}: Readonly<{
  label: string;
  value: number;
  step: string;
  min: number;
  onChange: (v: number) => void;
  hint?: string;
}>) {
  return (
    <div>
      <label className="field-label">{label}</label>
      <input
        type="number"
        className="input"
        value={value}
        step={step}
        min={min}
        onChange={(e) => {
          const v = Number(e.target.value);
          onChange(Number.isFinite(v) ? v : 0);
        }}
      />
      {hint && <div className="text-xs text-ink-500 mt-1">{hint}</div>}
    </div>
  );
}
