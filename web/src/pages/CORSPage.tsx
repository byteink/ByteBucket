import { useEffect, useState } from 'react';
import { CORSConfig, getCORS, putCORS } from '../lib/admin';
import { loadSession } from '../lib/session';

const empty: CORSConfig = {
  allowed_origins: [],
  allowed_methods: [],
  allowed_headers: [],
  expose_headers: [],
  max_age: 0,
};

export default function CORSPage() {
  const session = loadSession();
  const [cfg, setCfg] = useState<CORSConfig | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);

  useEffect(() => {
    if (!session) return;
    (async () => {
      try {
        setCfg(await getCORS(session));
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function setList(key: keyof CORSConfig, value: string) {
    if (!cfg) return;
    const arr = value
      .split('\n')
      .map((s) => s.trim())
      .filter((s) => s.length > 0);
    setCfg({ ...cfg, [key]: arr } as CORSConfig);
  }

  async function onSave() {
    if (!session || !cfg) return;
    setError(null);
    setStatus(null);
    try {
      await putCORS(session, cfg);
      setStatus('Saved.');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  const value = cfg ?? empty;

  return (
    <section className="max-w-2xl">
      <h2 className="text-base mb-6">CORS</h2>
      {error && <div className="text-xs text-ink-900 border-l-2 border-ink-900 pl-3 mb-4">{error}</div>}
      {status && <div className="text-xs text-ink-500 mb-4">{status}</div>}
      {cfg === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <div className="space-y-5">
          <ListField label="Allowed origins" value={value.allowed_origins} onChange={(v) => setList('allowed_origins', v)} />
          <ListField label="Allowed methods" value={value.allowed_methods} onChange={(v) => setList('allowed_methods', v)} />
          <ListField label="Allowed headers" value={value.allowed_headers} onChange={(v) => setList('allowed_headers', v)} />
          <ListField label="Expose headers" value={value.expose_headers} onChange={(v) => setList('expose_headers', v)} />
          <div>
            <label className="field-label">Max age (seconds)</label>
            <input
              type="number"
              className="input w-40"
              value={value.max_age}
              onChange={(e) => setCfg({ ...value, max_age: Number(e.target.value) || 0 })}
            />
          </div>
          <div className="flex justify-end">
            <button className="btn-primary" onClick={onSave}>Save</button>
          </div>
        </div>
      )}
    </section>
  );
}

function ListField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string[];
  onChange: (v: string) => void;
}) {
  return (
    <div>
      <label className="field-label">{label}</label>
      <textarea
        className="w-full h-24 border border-ink-200 p-2 font-mono text-xs focus:outline-none focus:border-ink-900"
        placeholder="one value per line"
        value={value.join('\n')}
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  );
}
