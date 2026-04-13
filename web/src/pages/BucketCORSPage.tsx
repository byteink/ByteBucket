import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  BucketCORSConfig,
  BucketCORSRule,
  deleteBucketCORS,
  getBucketCORS,
  NoSuchCORSConfiguration,
  putBucketCORS,
} from '../lib/s3';
import { loadSession } from '../lib/session';

// emptyRule is the seed used both for "Add rule" and for the first rule when
// a bucket has no CORS config yet. We use empty arrays rather than prefilled
// placeholders so round-tripping a freshly created config doesn't smuggle in
// values the user never typed.
const emptyRule: BucketCORSRule = {
  ID: '',
  AllowedMethods: [],
  AllowedOrigins: [],
  AllowedHeaders: [],
  ExposeHeaders: [],
  MaxAgeSeconds: 0,
};

export default function BucketCORSPage() {
  const { name } = useParams<{ name: string }>();
  const bucket = name ?? '';
  const session = loadSession();
  const [rules, setRules] = useState<BucketCORSRule[] | null>(null);
  const [exists, setExists] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);

  useEffect(() => {
    if (!session || !bucket) return;
    (async () => {
      try {
        const cfg = await getBucketCORS(session, bucket);
        setRules(cfg.CORSRules ?? []);
        setExists(true);
      } catch (e) {
        if (e instanceof NoSuchCORSConfiguration) {
          // Treat "no config" as a clean empty editor rather than an error so
          // the operator can author the first rule in place.
          setRules([]);
          setExists(false);
          return;
        }
        setError(e instanceof Error ? e.message : String(e));
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bucket]);

  function updateRule(i: number, patch: Partial<BucketCORSRule>) {
    if (!rules) return;
    setRules(rules.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  }

  function addRule() {
    setRules([...(rules ?? []), { ...emptyRule }]);
  }

  function removeRule(i: number) {
    if (!rules) return;
    setRules(rules.filter((_, idx) => idx !== i));
  }

  async function onSave() {
    if (!session || !rules) return;
    setError(null);
    setStatus(null);
    try {
      const cfg: BucketCORSConfig = { CORSRules: rules };
      await putBucketCORS(session, bucket, cfg);
      setExists(true);
      setStatus('Saved.');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onDeleteAll() {
    if (!session) return;
    if (!window.confirm(`Delete CORS configuration for ${bucket}?`)) return;
    setError(null);
    setStatus(null);
    try {
      await deleteBucketCORS(session, bucket);
      setRules([]);
      setExists(false);
      setStatus('Deleted.');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <section className="max-w-3xl">
      <nav className="text-xs text-ink-500 mb-2">
        <Link to="/buckets" className="hover:underline">Buckets</Link>
        <span className="mx-1">/</span>
        <span className="font-mono text-ink-900">{bucket}</span>
        <span className="mx-1">/</span>
        <span>CORS</span>
      </nav>
      <div className="flex items-baseline justify-between mb-6">
        <h2 className="text-base">CORS</h2>
        <div className="flex gap-2">
          <button className="btn" onClick={addRule} disabled={rules === null}>Add rule</button>
          {exists && (
            <button className="btn-danger" onClick={onDeleteAll}>Delete all</button>
          )}
          <button className="btn-primary" onClick={onSave} disabled={rules === null}>Save</button>
        </div>
      </div>

      {error && <div className="text-xs text-ink-900 border-l-2 border-ink-900 pl-3 mb-4">{error}</div>}
      {status && <div className="text-xs text-ink-500 mb-4">{status}</div>}

      {rules === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : rules.length === 0 ? (
        <p className="text-ink-500 text-sm">
          No rules. {exists ? 'Add a rule or delete the configuration.' : 'Add a rule and save to create a configuration.'}
        </p>
      ) : (
        <div className="space-y-8">
          {rules.map((r, i) => (
            <RuleEditor
              key={i}
              index={i}
              rule={r}
              onChange={(patch) => updateRule(i, patch)}
              onRemove={() => removeRule(i)}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function RuleEditor({
  index,
  rule,
  onChange,
  onRemove,
}: {
  index: number;
  rule: BucketCORSRule;
  onChange: (patch: Partial<BucketCORSRule>) => void;
  onRemove: () => void;
}) {
  return (
    <div className="border-t border-ink-200 pt-5">
      <div className="flex items-baseline justify-between mb-4">
        <h3 className="text-sm font-mono text-ink-500">Rule {index + 1}</h3>
        <button className="btn-danger h-7 px-2 text-xs" onClick={onRemove}>Remove</button>
      </div>
      <div className="space-y-5">
        <div>
          <label className="field-label">ID</label>
          <input
            className="input font-mono"
            value={rule.ID ?? ''}
            onChange={(e) => onChange({ ID: e.target.value })}
            placeholder="optional"
          />
        </div>
        <ListField
          label="Allowed origins"
          value={rule.AllowedOrigins}
          onChange={(v) => onChange({ AllowedOrigins: v })}
        />
        <ListField
          label="Allowed methods"
          value={rule.AllowedMethods}
          onChange={(v) => onChange({ AllowedMethods: v })}
        />
        <ListField
          label="Allowed headers"
          value={rule.AllowedHeaders ?? []}
          onChange={(v) => onChange({ AllowedHeaders: v })}
        />
        <ListField
          label="Expose headers"
          value={rule.ExposeHeaders ?? []}
          onChange={(v) => onChange({ ExposeHeaders: v })}
        />
        <div>
          <label className="field-label">Max age (seconds)</label>
          <input
            type="number"
            min={0}
            className="input w-40"
            value={rule.MaxAgeSeconds ?? 0}
            onChange={(e) => onChange({ MaxAgeSeconds: Number(e.target.value) || 0 })}
          />
        </div>
      </div>
    </div>
  );
}

function ListField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string[];
  onChange: (v: string[]) => void;
}) {
  // The textarea line-split reflects the S3 API shape (arrays of strings) while
  // giving the operator a familiar "one per line" editor. Blank lines are
  // discarded on every keystroke so the serialised payload stays clean.
  return (
    <div>
      <label className="field-label">{label}</label>
      <textarea
        className="w-full h-24 border border-ink-200 p-2 font-mono text-xs focus:outline-none focus:border-ink-900"
        placeholder="one value per line"
        value={value.join('\n')}
        onChange={(e) =>
          onChange(
            e.target.value
              .split('\n')
              .map((s) => s.trim())
              .filter((s) => s.length > 0),
          )
        }
      />
    </div>
  );
}
