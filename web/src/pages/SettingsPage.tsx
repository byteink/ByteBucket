import { useEffect, useState, type ReactNode } from 'react';
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
import { errorMessage } from '../lib/format';
import { ErrorBanner } from '../components/ErrorBanner';
import { Loading, PageHeader, Saved, Tip } from '../components/ui';

// Well-known reverse-proxy / CDN headers offered as presets. Operators can add
// any other header name; these cover the common vendors.
const PROXY_HEADER_PRESETS = ['X-Forwarded-For', 'X-Real-IP', 'CF-Connecting-IP', 'True-Client-IP', 'Fly-Client-IP'];

// Header names are case-insensitive (RFC 7230); compare accordingly.
function hasHeader(headers: string[], h: string): boolean {
  return headers.some((x) => x.toLowerCase() === h.toLowerCase());
}

// useSetting loads one setting once and owns the busy/error state of any
// write against it, so every section shares the same lifecycle without
// restating it.
function useSetting<T>(session: Session, load: (s: Session) => Promise<T>) {
  const [value, setValue] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let live = true;
    load(session)
      .then((v) => {
        if (live) setValue(v);
      })
      .catch((e) => {
        if (live) setError(errorMessage(e));
      });
    return () => {
      live = false;
    };
    // session is read once from localStorage; depending on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function run(fn: () => Promise<void>) {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  return { value, setValue, error, busy, run };
}

function Section({ title, desc, children }: Readonly<{ title: string; desc: string; children: ReactNode }>) {
  return (
    <section className="sec">
      <div>
        <h2>{title}</h2>
        <p className="desc">{desc}</p>
      </div>
      <div className="form">{children}</div>
    </section>
  );
}

function Check({
  label,
  checked,
  disabled = false,
  mono = false,
  onChange,
}: Readonly<{ label: string; checked: boolean; disabled?: boolean; mono?: boolean; onChange: (v: boolean) => void }>) {
  return (
    <label className="chk">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className={mono ? 'font-mono text-xs' : ''}>{label}</span>
    </label>
  );
}

function Num({
  id,
  label,
  value,
  step,
  min,
  max,
  hint,
  className = 'flex-1',
  onChange,
}: Readonly<{
  id: string;
  label: string;
  value: number;
  step: string;
  min: number;
  max?: number;
  hint?: string;
  className?: string;
  onChange: (v: number) => void;
}>) {
  return (
    <div className={className}>
      <label className="field-label" htmlFor={id}>
        {label}
      </label>
      <input
        id={id}
        type="number"
        className="input tabular-nums"
        value={value}
        step={step}
        min={min}
        max={max}
        onChange={(e) => {
          const v = Number(e.target.value);
          onChange(Number.isFinite(v) ? v : 0);
        }}
      />
      {hint && <div className="hint">{hint}</div>}
    </div>
  );
}

function SaveRow({
  busy,
  disabled = false,
  onSave,
  children,
}: Readonly<{ busy: boolean; disabled?: boolean; onSave: () => void; children?: ReactNode }>) {
  return (
    <div className="flex items-center gap-3">
      <button type="button" className="btn-primary" disabled={busy || disabled} onClick={onSave}>
        {busy ? 'Saving' : 'Save'}
      </button>
      {children}
    </div>
  );
}

function RateLimitSection({ session }: Readonly<{ session: Session }>) {
  const { value: state, setValue: setState, error, busy, run } = useSetting(session, getRateLimit);
  const [form, setForm] = useState<RateLimitConfig | null>(null);
  const draft = form ?? state?.effective ?? null;

  function edit(p: Partial<RateLimitConfig>) {
    if (draft) setForm({ ...draft, ...p });
  }

  function apply(next: RateLimitState) {
    setState(next);
    setForm(null);
  }

  function onSave() {
    if (!state || !draft) return;
    run(async () => {
      const effective = await putRateLimit(session, draft);
      apply({ env: state.env, override: draft, effective });
    });
  }

  function onReset() {
    if (!state) return;
    run(async () => {
      const effective = await deleteRateLimit(session);
      apply({ env: state.env, override: null, effective });
    });
  }

  return (
    <Section
      title="Rate limiting"
      desc="Per-client request cap on the S3 port. Overrides the RATE_LIMIT_* environment values."
    >
      {error && <ErrorBanner message={error} />}
      {!state || !draft ? (
        <Loading />
      ) : (
        <>
          <Check label="Enable request rate limiting" checked={draft.enabled} onChange={(v) => edit({ enabled: v })} />
          <div className="row">
            <Num
              id="rl-rps"
              label="Requests per second"
              value={draft.rps}
              step="0.1"
              min={0}
              hint={`Environment: ${state.env.rps}`}
              onChange={(v) => edit({ rps: v })}
            />
            <Num
              id="rl-burst"
              label="Burst"
              value={draft.burst}
              step="1"
              min={0}
              hint={`Environment: ${state.env.burst}`}
              onChange={(v) => edit({ burst: Math.trunc(v) })}
            />
          </div>
          <SaveRow busy={busy} onSave={onSave}>
            <Tip text="Drop the override and use the environment values">
              <button type="button" className="btn" disabled={busy || !state.override} onClick={onReset}>
                Reset to environment
              </button>
            </Tip>
            <span className="ml-auto">
              <Saved text={state.override ? 'Runtime override' : 'Environment values'} />
            </span>
          </SaveRow>
        </>
      )}
    </Section>
  );
}

function LiveCheck({ who, onRefresh }: Readonly<{ who: WhoAmI | null; onRefresh: () => void }>) {
  const mismatch =
    who !== null && who.trustedHeaders.length > 0 && who.detectedHeader !== '' && !hasHeader(who.trustedHeaders, who.detectedHeader);
  return (
    <div className="note border-ink-200 text-ink-500 flex flex-col gap-0.5">
      <div className="flex items-center justify-between">
        <span>Live check · this request</span>
        <button type="button" className="btn-ghost btn-sm -mr-2" onClick={onRefresh}>
          Refresh
        </button>
      </div>
      {who && (
        <>
          <div>
            Server sees you as <span className="font-mono text-ink-900">{who.ip}</span>
            {who.detectedHeader ? (
              <>
                {' '}
                via <span className="font-mono">{who.detectedHeader}</span>
              </>
            ) : (
              ' (no proxy header detected)'
            )}
          </div>
          <div>
            Socket peer <span className="font-mono">{who.remoteAddr}</span> · X-Forwarded-For{' '}
            <span className="font-mono">{who.forwardedFor || '—'}</span>
          </div>
          {mismatch && (
            <div className="text-danger">
              Configured header does not match the detected one ({who.detectedHeader}); the captured IP may be the
              proxy, not the client.
            </div>
          )}
        </>
      )}
    </div>
  );
}

function TrustedProxySection({ session }: Readonly<{ session: Session }>) {
  const { value: cfg, setValue: setCfg, error, busy, run } = useSetting(session, getTrustedProxy);
  const [form, setForm] = useState<TrustedProxyConfig | null>(null);
  const [who, setWho] = useState<WhoAmI | null>(null);
  const [whoError, setWhoError] = useState<string | null>(null);
  const [custom, setCustom] = useState('');
  const [saved, setSaved] = useState(false);
  const draft = form ?? cfg;

  function loadWho() {
    getWhoAmI(session)
      .then((w) => {
        setWho(w);
        setWhoError(null);
      })
      .catch((e) => setWhoError(errorMessage(e)));
  }

  useEffect(() => {
    loadWho();
    // session is read once from localStorage; depending on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function edit(p: Partial<TrustedProxyConfig>) {
    if (!draft) return;
    setForm({ ...draft, ...p });
    setSaved(false);
  }

  function toggle(h: string) {
    if (!draft) return;
    const on = hasHeader(draft.headers, h);
    edit({ headers: on ? draft.headers.filter((x) => x.toLowerCase() !== h.toLowerCase()) : [...draft.headers, h] });
  }

  function addCustom() {
    const h = custom.trim();
    if (!draft || h === '') return;
    if (!hasHeader(draft.headers, h)) edit({ headers: [...draft.headers, h] });
    setCustom('');
  }

  function onSave() {
    if (!draft) return;
    run(async () => {
      setCfg(await putTrustedProxy(session, draft));
      setForm(null);
      setSaved(true);
      loadWho();
    });
  }

  // The checkbox set is the presets plus any custom header already configured,
  // so every selected header (preset or custom) can be toggled off.
  const options = draft
    ? [...PROXY_HEADER_PRESETS, ...draft.headers.filter((h) => !hasHeader(PROXY_HEADER_PRESETS, h))]
    : [];

  return (
    <Section
      title="Trusted proxy"
      desc="Headers trusted to carry the real client IP behind a reverse proxy. First present header wins. Empty trusts no header."
    >
      {error && <ErrorBanner message={error} />}
      {whoError && <ErrorBanner message={whoError} />}
      {!draft ? (
        <Loading />
      ) : (
        <>
          <div className="grid grid-cols-2 gap-x-4 gap-y-2">
            {options.map((h) => (
              <Check key={h} label={h} mono checked={hasHeader(draft.headers, h)} disabled={busy} onChange={() => toggle(h)} />
            ))}
          </div>
          <div className="row">
            <div className="flex-1">
              <label className="field-label" htmlFor="tp-custom">
                Custom header
              </label>
              <input
                id="tp-custom"
                className="input-mono"
                value={custom}
                placeholder="X-Client-IP"
                onChange={(e) => setCustom(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key !== 'Enter') return;
                  e.preventDefault();
                  addCustom();
                }}
              />
            </div>
            <button type="button" className="btn self-end" disabled={busy || custom.trim() === ''} onClick={addCustom}>
              Add
            </button>
          </div>
          <Check
            label="Use leftmost IP of a multi-value header (less safe)"
            checked={draft.useLeftmostIP}
            disabled={busy}
            onChange={(v) => edit({ useLeftmostIP: v })}
          />
          <LiveCheck who={who} onRefresh={loadWho} />
          <SaveRow busy={busy} onSave={onSave}>
            {saved && form === null && <Saved />}
          </SaveRow>
        </>
      )}
    </Section>
  );
}

function DurabilitySection({ session }: Readonly<{ session: Session }>) {
  const { value: on, setValue: setOn, error, busy, run } = useSetting(session, getSyncWrites);
  const [draft, setDraft] = useState<boolean | null>(null);
  const [savedText, setSavedText] = useState<string | null>(null);
  const checked = draft ?? on;

  function onSave() {
    if (checked === null) return;
    run(async () => {
      const now = await putSyncWrites(session, checked);
      setOn(now);
      setDraft(null);
      setSavedText(now ? 'Durable writes enabled (fsync on).' : 'Durable writes disabled (faster, less safe).');
    });
  }

  return (
    <Section title="Durability" desc="Whether each upload and copy is flushed to disk before the response returns.">
      {error && <ErrorBanner message={error} />}
      {checked === null ? (
        <Loading />
      ) : (
        <>
          <Check
            label="Sync writes to disk (fsync)"
            checked={checked}
            disabled={busy}
            onChange={(v) => {
              setDraft(v);
              setSavedText(null);
            }}
          />
          <p className="hint mt-0">
            On: an acknowledged write survives power loss. Off: faster writes that may be lost on a crash.
          </p>
          <SaveRow busy={busy} disabled={checked === on} onSave={onSave}>
            {savedText && <Saved text={savedText} />}
          </SaveRow>
        </>
      )}
    </Section>
  );
}

function AccessLogSection({ session }: Readonly<{ session: Session }>) {
  const { value: cfg, setValue: setCfg, error, busy, run } = useSetting(session, getAccessLog);
  const [form, setForm] = useState<AccessLogConfig | null>(null);
  const [saved, setSaved] = useState(false);
  const draft = form ?? cfg;

  function edit(p: Partial<AccessLogConfig>) {
    if (!draft) return;
    setForm({ ...draft, ...p });
    setSaved(false);
  }

  function onSave() {
    if (!draft) return;
    run(async () => {
      setCfg(await putAccessLog(session, draft));
      setForm(null);
      setSaved(true);
    });
  }

  return (
    <Section
      title="Access log"
      desc="Record every object read, write and delete to the unified log. Written off the request path."
    >
      {error && <ErrorBanner message={error} />}
      {!draft ? (
        <Loading />
      ) : (
        <>
          <Check label="Record object access" checked={draft.enabled} disabled={busy} onChange={(v) => edit({ enabled: v })} />
          <div className="row">
            <Num
              id="al-max-events"
              label="Max events"
              value={draft.maxEvents}
              step="1000"
              min={0}
              hint="0 = no count cap"
              onChange={(v) => edit({ maxEvents: Math.trunc(v) })}
            />
            <Num
              id="al-max-age"
              label="Max age (days)"
              value={draft.maxAgeDays}
              step="1"
              min={0}
              max={365}
              hint="0 = no age cap · whichever hits first prunes"
              onChange={(v) => edit({ maxAgeDays: Math.trunc(v) })}
            />
          </div>
          <SaveRow busy={busy} onSave={onSave}>
            {saved && form === null && <Saved />}
          </SaveRow>
        </>
      )}
    </Section>
  );
}

function RetentionSection({ session }: Readonly<{ session: Session }>) {
  const { value: days, setValue: setDays, error, busy, run } = useSetting(session, getRetention);
  const [draft, setDraft] = useState<number | null>(null);
  const [savedText, setSavedText] = useState<string | null>(null);
  const value = draft ?? days;

  function onSave() {
    if (value === null) return;
    run(async () => {
      const saved = await putRetention(session, value);
      setDays(saved);
      setDraft(null);
      setSavedText(`Request history retained for ${saved} days.`);
    });
  }

  return (
    <Section
      title="Metrics retention"
      desc="How long the Overview keeps per-minute request history. Older samples are pruned."
    >
      {error && <ErrorBanner message={error} />}
      {value === null ? (
        <Loading />
      ) : (
        <>
          <div className="row">
            <Num
              id="retention-days"
              label="Retention (days)"
              value={value}
              step="1"
              min={1}
              max={365}
              className="w-40"
              onChange={(v) => {
                setDraft(Math.trunc(v));
                setSavedText(null);
              }}
            />
            <span className="text-xs text-ink-500 self-end leading-8">1–365</span>
          </div>
          <SaveRow busy={busy} disabled={value === days} onSave={onSave}>
            {savedText && <Saved text={savedText} />}
          </SaveRow>
        </>
      )}
    </Section>
  );
}

export default function SettingsPage() {
  const session = loadSession();
  if (!session) return null;
  return (
    <div>
      <PageHeader
        title="Settings"
        sub="Runtime configuration. Saved values apply immediately on both ports and persist across restarts."
      />
      <RateLimitSection session={session} />
      <TrustedProxySection session={session} />
      <DurabilitySection session={session} />
      <AccessLogSection session={session} />
      <RetentionSection session={session} />
    </div>
  );
}
