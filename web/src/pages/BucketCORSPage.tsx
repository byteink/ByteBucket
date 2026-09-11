import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  type BucketCORSConfig,
  deleteBucketCORS,
  getBucketCORS,
  NoSuchCORSConfiguration,
  putBucketCORS,
} from '../lib/s3';
import { loadSession } from '../lib/session';
import { errorMessage } from '../lib/format';
import { ErrorBanner } from '../components/ErrorBanner';
import { ConfirmDialog, Loading, PageHeader, Saved } from '../components/ui';

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

const defaultText = JSON.stringify(defaultConfig, null, 2);

function parseConfig(text: string): BucketCORSConfig {
  try {
    return JSON.parse(text) as BucketCORSConfig;
  } catch (e) {
    throw new Error(`Invalid JSON: ${errorMessage(e)}`);
  }
}

export default function BucketCORSPage() {
  const { name } = useParams<{ name: string }>();
  const bucket = name ?? '';
  const [session] = useState(loadSession);
  const [text, setText] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [exists, setExists] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<'saved' | 'deleted' | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);

  useEffect(() => {
    if (!session || !bucket) return;
    (async () => {
      try {
        setText(JSON.stringify(await getBucketCORS(session, bucket), null, 2));
        setExists(true);
      } catch (e) {
        if (!(e instanceof NoSuchCORSConfiguration)) setError(errorMessage(e));
        setText(defaultText);
        setExists(false);
      } finally {
        setLoaded(true);
      }
    })();
  }, [session, bucket]);

  async function onSave() {
    if (!session) return;
    setError(null);
    setStatus(null);
    try {
      await putBucketCORS(session, bucket, parseConfig(text));
      setExists(true);
      setStatus('saved');
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function onDelete() {
    if (!session) return;
    setBusy(true);
    setDialogError(null);
    try {
      await deleteBucketCORS(session, bucket);
      setText(defaultText);
      setExists(false);
      setStatus('deleted');
      setConfirming(false);
    } catch (e) {
      setDialogError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  function onEdit(value: string) {
    setText(value);
    setStatus(null);
  }

  return (
    <section className="max-w-3xl">
      <nav className="crumb" aria-label="Breadcrumb">
        <Link to="/buckets">Buckets</Link>
        <span>/</span>
        <span className="here">{bucket}</span>
        <span>/</span>
        <span>CORS</span>
      </nav>
      <PageHeader
        title="CORS"
        sub="Cross-origin rules for browser clients. JSON, same shape as the S3 PutBucketCors body."
        actions={
          <>
            {status === 'saved' && <Saved />}
            {status === 'deleted' && <Saved text="Deleted" />}
            {exists && (
              <button type="button" className="btn-danger" onClick={() => setConfirming(true)}>
                Delete
              </button>
            )}
            <button type="button" className="btn-primary" onClick={onSave} disabled={!loaded}>
              Save
            </button>
          </>
        }
      />

      {error && <ErrorBanner message={error} className="mb-4" />}

      {loaded ? (
        <div>
          <label className="field-label" htmlFor="cors-rules">
            Rules
          </label>
          <textarea
            id="cors-rules"
            className="input-mono h-[28rem] p-3 resize-y"
            spellCheck={false}
            value={text}
            onChange={(e) => onEdit(e.target.value)}
          />
        </div>
      ) : (
        <Loading />
      )}

      <ConfirmDialog
        open={confirming}
        title={
          <>
            Delete CORS configuration for <span className="font-mono">{bucket}</span>?
          </>
        }
        body="Browser clients from other origins will no longer be able to reach this bucket."
        confirmLabel="Delete configuration"
        busy={busy}
        error={dialogError}
        onConfirm={onDelete}
        onClose={() => !busy && setConfirming(false)}
      />
    </section>
  );
}
