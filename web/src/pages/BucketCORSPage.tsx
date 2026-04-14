import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  BucketCORSConfig,
  deleteBucketCORS,
  getBucketCORS,
  NoSuchCORSConfiguration,
  putBucketCORS,
} from '../lib/s3';
import { loadSession } from '../lib/session';

const defaultConfig: BucketCORSConfig = {
  CORSRules: [
    {
      AllowedMethods: ['GET'],
      AllowedOrigins: ['*'],
      AllowedHeaders: [],
      ExposeHeaders: [],
      MaxAgeSeconds: 3000,
    },
  ],
};

export default function BucketCORSPage() {
  const { name } = useParams<{ name: string }>();
  const bucket = name ?? '';
  const session = loadSession();
  const [text, setText] = useState<string>('');
  const [loaded, setLoaded] = useState(false);
  const [exists, setExists] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);

  useEffect(() => {
    if (!session || !bucket) return;
    (async () => {
      try {
        const cfg = await getBucketCORS(session, bucket);
        setText(JSON.stringify(cfg, null, 2));
        setExists(true);
      } catch (e) {
        if (e instanceof NoSuchCORSConfiguration) {
          setText(JSON.stringify(defaultConfig, null, 2));
          setExists(false);
        } else {
          setError(e instanceof Error ? e.message : String(e));
        }
      } finally {
        setLoaded(true);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bucket]);

  async function onSave() {
    if (!session) return;
    setError(null);
    setStatus(null);
    let parsed: BucketCORSConfig;
    try {
      parsed = JSON.parse(text);
    } catch (e) {
      setError(`Invalid JSON: ${e instanceof Error ? e.message : String(e)}`);
      return;
    }
    try {
      await putBucketCORS(session, bucket, parsed);
      setExists(true);
      setStatus('Saved.');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onDelete() {
    if (!session) return;
    if (!window.confirm(`Delete CORS configuration for ${bucket}?`)) return;
    setError(null);
    setStatus(null);
    try {
      await deleteBucketCORS(session, bucket);
      setText(JSON.stringify(defaultConfig, null, 2));
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
          {exists && (
            <button className="btn-danger" onClick={onDelete}>Delete</button>
          )}
          <button className="btn-primary" onClick={onSave} disabled={!loaded}>Save</button>
        </div>
      </div>

      {error && <div className="text-xs text-ink-900 border-l-2 border-ink-900 pl-3 mb-4 whitespace-pre-wrap">{error}</div>}
      {status && <div className="text-xs text-ink-500 mb-4">{status}</div>}

      {loaded ? (
        <textarea
          className="w-full h-[28rem] border border-ink-200 p-3 font-mono text-xs focus:outline-none focus:border-ink-900"
          spellCheck={false}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
      ) : (
        <p className="text-ink-500 text-sm">Loading.</p>
      )}
    </section>
  );
}
