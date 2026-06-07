import { useEffect, useState } from 'react';
import {
  deleteRateLimit,
  getRateLimit,
  getRetention,
  getSyncWrites,
  putRateLimit,
  putRetention,
  putSyncWrites,
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
  const [syncOn, setSyncOn] = useState<boolean | null>(null);
  const [syncBusy, setSyncBusy] = useState(false);
  const [retentionDays, setRetentionDays] = useState<number | null>(null);
  const [retentionBusy, setRetentionBusy] = useState(false);

  useEffect(() => {
    if (!session) return;
    getRateLimit(session)
      .then((s) => {
        setState(s);
        setForm(s.effective);
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
    getSyncWrites(session)
      .then(setSyncOn)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
    getRetention(session)
      .then(setRetentionDays)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
    // session is read once from localStorage; refetching on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function onSaveRetention(days: number) {
    if (!session) return;
    setError(null);
    setNotice(null);
    setRetentionBusy(true);
    try {
      const saved = await putRetention(session, days);
      setRetentionDays(saved);
      setNotice(`Request history retained for ${saved} days.`);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setRetentionBusy(false);
    }
  }

  async function onToggleSync(enabled: boolean) {
    if (!session) return;
    setError(null);
    setNotice(null);
    setSyncBusy(true);
    try {
      const now = await putSyncWrites(session, enabled);
      setSyncOn(now);
      setNotice(now ? 'Durable writes enabled (fsync on).' : 'Durable writes disabled (faster, less safe).');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSyncBusy(false);
    }
  }

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

      <div className="mb-2 mt-8 flex items-baseline justify-between">
        <h3 className="text-sm">Durability</h3>
      </div>
      {syncOn === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <div className="border border-ink-200 p-4 max-w-md">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={syncOn}
              disabled={syncBusy}
              onChange={(e) => onToggleSync(e.target.checked)}
            />
            <span>Sync writes to disk (fsync)</span>
          </label>
          <p className="text-xs text-ink-500 mt-2">
            On: each upload and copy is flushed to disk before the response returns, so an
            acknowledged write survives power loss. Off: faster writes that may be lost on a crash.
          </p>
        </div>
      )}

      <div className="mb-2 mt-8 flex items-baseline justify-between">
        <h3 className="text-sm">Metrics retention</h3>
      </div>
      {retentionDays === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <div className="border border-ink-200 p-4 max-w-md">
          <RetentionField days={retentionDays} busy={retentionBusy} onSave={onSaveRetention} />
          <p className="text-xs text-ink-500 mt-2">
            How long the dashboard keeps per-minute request-outcome history for the 2xx/4xx/5xx
            chart. Older samples are pruned. Range 1–365 days.
          </p>
        </div>
      )}
    </section>
  );
}

function RetentionField({
  days,
  busy,
  onSave,
}: Readonly<{ days: number; busy: boolean; onSave: (days: number) => void }>) {
  const [value, setValue] = useState(days);
  return (
    <div className="flex items-end gap-2">
      <div className="flex-1">
        <label className="field-label" htmlFor="retention-days">Retention (days)</label>
        <input
          id="retention-days"
          type="number"
          className="input"
          value={value}
          step="1"
          min={1}
          max={365}
          onChange={(e) => {
            const v = Number(e.target.value);
            setValue(Number.isFinite(v) ? Math.trunc(v) : days);
          }}
        />
      </div>
      <button
        className="btn-primary h-9 px-3 text-xs"
        disabled={busy || value === days}
        onClick={() => onSave(value)}
      >
        {busy ? 'Saving' : 'Save'}
      </button>
    </div>
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
  const id = `nf-${label.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`;
  return (
    <div>
      <label className="field-label" htmlFor={id}>{label}</label>
      <input
        id={id}
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
