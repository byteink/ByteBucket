import { useEffect, useState } from 'react';
import {
  deleteRateLimit,
  getAccessLog,
  getRateLimit,
  getRetention,
  getSyncWrites,
  getTrustedProxy,
  getWhoAmI,
  putAccessLog,
  putRateLimit,
  putRetention,
  putSyncWrites,
  putTrustedProxy,
  type AccessLogConfig,
  type RateLimitConfig,
  type RateLimitState,
  type TrustedProxyConfig,
  type WhoAmI,
} from '../lib/admin';
import { loadSession, type Session } from '../lib/session';
import { ErrorBanner } from '../components/ErrorBanner';

// Well-known reverse-proxy / CDN headers offered as presets. Operators can add
// any other header name; these cover the common vendors.
const PROXY_HEADER_PRESETS = ['X-Forwarded-For', 'X-Real-IP', 'CF-Connecting-IP', 'True-Client-IP', 'Fly-Client-IP'];

// Header names are case-insensitive (RFC 7230); compare accordingly.
function hasHeader(headers: string[], h: string): boolean {
  return headers.some((x) => x.toLowerCase() === h.toLowerCase());
}

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
  const [accessLog, setAccessLog] = useState<AccessLogConfig | null>(null);
  const [accessBusy, setAccessBusy] = useState(false);

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
    getAccessLog(session)
      .then(setAccessLog)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
    // session is read once from localStorage; refetching on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function patchAccess(p: Partial<AccessLogConfig>) {
    setAccessLog((f) => (f ? { ...f, ...p } : f));
  }

  async function onSaveAccessLog() {
    if (!session || !accessLog) return;
    setError(null);
    setNotice(null);
    setAccessBusy(true);
    try {
      const saved = await putAccessLog(session, accessLog);
      setAccessLog(saved);
      setNotice(saved.enabled ? 'Access logging enabled.' : 'Access logging disabled.');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setAccessBusy(false);
    }
  }

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
        <h3 className="text-sm">Trusted proxy</h3>
      </div>
      {session && <TrustedProxySection session={session} onNotice={setNotice} onError={setError} />}

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

      <div className="mb-2 mt-8 flex items-baseline justify-between">
        <h3 className="text-sm">Access log</h3>
      </div>
      {accessLog === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <div className="border border-ink-200 p-4 max-w-md space-y-4">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={accessLog.enabled}
              disabled={accessBusy}
              onChange={(e) => patchAccess({ enabled: e.target.checked })}
            />
            <span>Record object access (data plane)</span>
          </label>

          <NumberField
            label="Max events (0 = no count cap)"
            value={accessLog.maxEvents}
            step="1000"
            min={0}
            onChange={(v) => patchAccess({ maxEvents: Math.trunc(v) })}
          />
          <NumberField
            label="Max age in days (0 = no age cap)"
            value={accessLog.maxAgeDays}
            step="1"
            min={0}
            onChange={(v) => patchAccess({ maxAgeDays: Math.trunc(v) })}
            hint="Range 0–365. Both caps apply; whichever is hit first prunes."
          />

          <button className="btn-primary h-8 px-3 text-xs" disabled={accessBusy} onClick={onSaveAccessLog}>
            {accessBusy ? 'Saving' : 'Save'}
          </button>
          <p className="text-xs text-ink-500">
            Logs every object read, write, and delete (who, which key, status) to the unified log,
            viewable under Logs. Off by default; the firehose is written off the request path. The
            captured client IP follows the Trusted proxy settings above.
          </p>
        </div>
      )}
    </section>
  );
}

function TrustedProxySection({
  session,
  onNotice,
  onError,
}: Readonly<{ session: Session; onNotice: (m: string) => void; onError: (m: string) => void }>) {
  const [cfg, setCfg] = useState<TrustedProxyConfig | null>(null);
  const [who, setWho] = useState<WhoAmI | null>(null);
  const [busy, setBusy] = useState(false);
  const [custom, setCustom] = useState('');

  function fail(e: unknown) {
    onError(e instanceof Error ? e.message : String(e));
  }
  function loadWho() {
    getWhoAmI(session).then(setWho).catch(fail);
  }

  useEffect(() => {
    getTrustedProxy(session).then(setCfg).catch(fail);
    loadWho();
    // session is read once; depending on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function toggle(h: string) {
    setCfg((c) =>
      c ? { ...c, headers: hasHeader(c.headers, h) ? c.headers.filter((x) => x.toLowerCase() !== h.toLowerCase()) : [...c.headers, h] } : c,
    );
  }
  function addCustom() {
    const h = custom.trim();
    if (!h) return;
    setCfg((c) => (c && !hasHeader(c.headers, h) ? { ...c, headers: [...c.headers, h] } : c));
    setCustom('');
  }

  async function onSave() {
    if (!cfg) return;
    onError('');
    onNotice('');
    setBusy(true);
    try {
      const saved = await putTrustedProxy(session, cfg);
      setCfg(saved);
      onNotice(saved.headers.length ? `Trusting headers: ${saved.headers.join(', ')}.` : 'No trusted headers; using the socket peer.');
      loadWho();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  }

  if (!cfg) return <p className="text-ink-500 text-sm">Loading.</p>;

  // The checkbox set is the presets plus any custom header already configured,
  // so every selected header (preset or custom) can be toggled off.
  const options = [...PROXY_HEADER_PRESETS, ...cfg.headers.filter((h) => !PROXY_HEADER_PRESETS.some((p) => p.toLowerCase() === h.toLowerCase()))];
  const mismatch =
    who !== null && who.trustedHeaders.length > 0 && who.detectedHeader !== '' && !hasHeader(who.trustedHeaders, who.detectedHeader);

  return (
    <div className="border border-ink-200 p-4 max-w-md space-y-4">
      <div className="space-y-1">
        {options.map((h) => (
          <label key={h} className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={hasHeader(cfg.headers, h)} disabled={busy} onChange={() => toggle(h)} />
            <span className="font-mono text-xs">{h}</span>
          </label>
        ))}
      </div>

      <div className="flex items-end gap-2">
        <div className="flex-1">
          <label className="field-label" htmlFor="tp-custom">Add custom header</label>
          <input
            id="tp-custom"
            className="input"
            value={custom}
            placeholder="X-Real-IP"
            onChange={(e) => setCustom(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                addCustom();
              }
            }}
          />
        </div>
        <button className="btn h-9 px-3 text-xs" disabled={busy || custom.trim() === ''} onClick={addCustom}>
          Add
        </button>
      </div>

      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={cfg.useLeftmostIP}
          disabled={busy}
          onChange={(e) => setCfg((c) => (c ? { ...c, useLeftmostIP: e.target.checked } : c))}
        />
        <span>Use leftmost IP (less safe)</span>
      </label>

      <button className="btn-primary h-8 px-3 text-xs" disabled={busy} onClick={onSave}>
        {busy ? 'Saving' : 'Save'}
      </button>

      <p className="text-xs text-ink-500">
        Ordered list of headers trusted to carry the real client IP behind a reverse proxy; the first
        present header wins. Empty means trust no header (use the socket peer). Rightmost is the safe
        default for a multi-value header like X-Forwarded-For.
      </p>

      {who && (
        <div className="text-xs text-ink-500 border-l-2 border-ink-300 pl-3 space-y-1">
          <div className="flex items-center justify-between">
            <span>Validation</span>
            <button className="text-ink-500 hover:text-ink-900" onClick={loadWho} type="button">
              Refresh
            </button>
          </div>
          <div>
            Your IP, as the server sees it: <span className="font-mono text-ink-900">{who.ip}</span>
            {who.detectedHeader ? <> via <span className="font-mono">{who.detectedHeader}</span></> : ' (no proxy header detected)'}
          </div>
          <div>
            Socket peer: <span className="font-mono">{who.remoteAddr}</span> · X-Forwarded-For:{' '}
            <span className="font-mono">{who.forwardedFor || '—'}</span>
          </div>
          {mismatch && (
            <div className="text-danger">
              Configured header does not match the detected one ({who.detectedHeader}); the captured IP
              may be the proxy, not the client.
            </div>
          )}
        </div>
      )}
    </div>
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
