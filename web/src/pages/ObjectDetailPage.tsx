import { useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  deleteObject,
  getBucketACL,
  getConfig,
  getObject,
  getObjectMetadata,
  presignObject,
  putObjectACL,
  type CannedACL,
  type ObjectMetadata,
  type PresignedURL,
} from '../lib/s3';
import { loadSession } from '../lib/session';
import { copyText } from '../lib/clipboard';
import { ErrorBanner } from '../components/ErrorBanner';

interface ObjectState {
  meta: ObjectMetadata;
  bucketACL: CannedACL;
  publicBaseURL: string;
}

export default function ObjectDetailPage() {
  const params = useParams();
  const bucket = params.name ?? '';
  // React Router stores the splat under params['*']; it carries slashes that
  // are part of the object key. Trim a stray leading slash so the wire-shape
  // key matches what other handlers store.
  const key = (params['*'] ?? '').replace(/^\/+/, '');
  const session = loadSession();
  const [state, setState] = useState<ObjectState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [previewURL, setPreviewURL] = useState<string | null>(null);
  const [previewText, setPreviewText] = useState<string | null>(null);
  const [presigned, setPresigned] = useState<PresignedURL | null>(null);
  const [presignTTL, setPresignTTL] = useState<number>(900);

  useEffect(() => {
    if (!session || !bucket || !key) return;
    let revokeURL: string | null = null;
    (async () => {
      try {
        const [meta, bAcl, cfg] = await Promise.all([
          getObjectMetadata(session, bucket, key),
          getBucketACL(session, bucket),
          getConfig(session),
        ]);
        setState({ meta, bucketACL: bAcl, publicBaseURL: cfg.publicBaseURL });

        // Eagerly fetch the body for renderable content types. Anything
        // outside the renderable set is skipped — we never download large
        // archives just to render a "no preview" panel.
        const contentType = (meta['content-type'] ?? '').toLowerCase();
        if (isPreviewable(contentType)) {
          const blob = await getObject(session, bucket, key);
          if (isTextLike(contentType)) {
            // Cap text preview so a 50 MB log file does not lock the tab.
            const slice = blob.slice(0, 256 * 1024);
            setPreviewText(await slice.text());
          } else {
            const url = URL.createObjectURL(blob);
            revokeURL = url;
            setPreviewURL(url);
          }
        }
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    })();
    return () => {
      if (revokeURL) URL.revokeObjectURL(revokeURL);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bucket, key]);

  const effectiveACL = useMemo<CannedACL>(() => {
    if (!state) return 'private';
    const own = (state.meta['acl'] ?? '').toLowerCase();
    if (own === 'public-read' || own === 'private') return own;
    return state.bucketACL;
  }, [state]);

  const shareURL = useMemo<string>(() => {
    if (!state) return '';
    const base = state.publicBaseURL || `${globalThis.location.protocol}//${globalThis.location.hostname}`;
    const keyPath = key.split('/').map((p) => encodeURIComponent(p)).join('/');
    return `${base}/${encodeURIComponent(bucket)}/${keyPath}`;
  }, [state, bucket, key]);

  async function onDownload() {
    if (!session) return;
    try {
      const blob = await getObject(session, bucket, key);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = key.split('/').pop() ?? key;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onToggleACL() {
    if (!session || !state) return;
    const next: CannedACL = effectiveACL === 'public-read' ? 'private' : 'public-read';
    if (next === 'public-read') {
      const ok = globalThis.confirm(
        `Make "${key}" public?\n\nAnyone with the URL will be able to download this object. Continue?`,
      );
      if (!ok) return;
    }
    try {
      await putObjectACL(session, bucket, key, next);
      const meta = await getObjectMetadata(session, bucket, key);
      setState({ ...state, meta });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onDelete() {
    if (!session) return;
    if (!globalThis.confirm(`Delete ${key}?`)) return;
    try {
      await deleteObject(session, bucket, key);
      globalThis.history.back();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onCopy() {
    try {
      await copyText(shareURL);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onPresign() {
    if (!session) return;
    try {
      const p = await presignObject(session, bucket, key, presignTTL);
      setPresigned(p);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onCopyPresigned() {
    if (!presigned) return;
    try {
      await copyText(presigned.url);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <section>
      <nav className="text-xs text-ink-500 mb-2">
        <Link to="/buckets" className="hover:underline">Buckets</Link>
        <span className="mx-1">/</span>
        <Link to={`/buckets/${encodeURIComponent(bucket)}/objects`} className="hover:underline">
          {bucket}
        </Link>
        <span className="mx-1">/</span>
        <span className="font-mono text-ink-900 break-all">{key}</span>
      </nav>

      <div className="flex items-baseline justify-between mb-6">
        <h2 className="text-base break-all">{key.split('/').pop()}</h2>
        <div className="flex gap-2">
          <button className="btn h-8 px-3 text-xs" onClick={onToggleACL}>
            {effectiveACL === 'public-read' ? 'Make private' : 'Make public'}
          </button>
          <button className="btn h-8 px-3 text-xs" onClick={onDownload}>Download</button>
          <button className="btn-danger h-8 px-3 text-xs" onClick={onDelete}>Delete</button>
        </div>
      </div>

      {error && <ErrorBanner message={error} className="mb-4" />}

      {!state ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-6 mb-6 text-xs">
            <Field label="Size">{formatSize(parseInt(state.meta['content-length'] ?? '0', 10))}</Field>
            <Field label="Content-Type">{state.meta['content-type'] ?? 'application/octet-stream'}</Field>
            <Field label="ETag">{(state.meta['etag'] ?? '').replace(/"/g, '')}</Field>
            <Field label="Last-Modified">{state.meta['last-modified'] ?? '-'}</Field>
            <Field label="Visibility">
              {effectiveACL === 'public-read' ? (
                <span className="inline-block px-2 py-0.5 border border-ink-900 text-ink-900 uppercase tracking-wide text-[10px]">
                  {state.meta['acl'] ? 'Public' : 'Public (inherited)'}
                </span>
              ) : (
                <span className="text-ink-500">Private</span>
              )}
            </Field>
            <Field label="CRC32">{state.meta['x-amz-checksum-crc32'] ?? '-'}</Field>
          </div>

          <div className="mb-6">
            <div className="text-xs text-ink-500 mb-1">Public URL</div>
            <div className="flex gap-2">
              <input
                readOnly
                value={shareURL}
                className="input flex-1 font-mono text-xs"
                onFocus={(e) => e.currentTarget.select()}
              />
              <button className="btn h-8 px-3 text-xs" onClick={onCopy}>Copy</button>
            </div>
            {effectiveACL !== 'public-read' && (
              <div className="text-xs text-ink-500 mt-1">
                Object is private. Make it public, or generate a presigned URL via the S3 SDK, for the link to work.
              </div>
            )}
            {!state.publicBaseURL && (
              <div className="text-xs text-ink-500 mt-1">
                Set <span className="font-mono">PUBLIC_BASE_URL</span> on the server to publish links under your own domain.
              </div>
            )}
          </div>

          <div className="mb-6">
            <div className="text-xs text-ink-500 mb-1">Presigned URL</div>
            <div className="flex gap-2 items-center">
              <select
                className="input h-8 text-xs"
                value={presignTTL}
                onChange={(e) => setPresignTTL(Number(e.target.value))}
              >
                <option value={300}>5 minutes</option>
                <option value={900}>15 minutes</option>
                <option value={3600}>1 hour</option>
                <option value={86400}>1 day</option>
                <option value={604800}>7 days</option>
              </select>
              <button className="btn h-8 px-3 text-xs" onClick={onPresign}>
                Generate
              </button>
            </div>
            {presigned && (
              <div className="mt-2">
                <div className="flex gap-2">
                  <input
                    readOnly
                    value={presigned.url}
                    className="input flex-1 font-mono text-xs"
                    onFocus={(e) => e.currentTarget.select()}
                  />
                  <button className="btn h-8 px-3 text-xs" onClick={onCopyPresigned}>Copy</button>
                </div>
                <div className="text-xs text-ink-500 mt-1">
                  Expires {new Date(presigned.expiresAt).toLocaleString()}. Works even when the object is private.
                </div>
              </div>
            )}
          </div>

          <Preview
            contentType={(state.meta['content-type'] ?? '').toLowerCase()}
            url={previewURL}
            text={previewText}
          />
        </>
      )}
    </section>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-ink-500 mb-0.5">{label}</div>
      <div className="font-mono break-all">{children}</div>
    </div>
  );
}

// Preview decides how to render the object based on Content-Type. Everything
// outside the explicit allowlist falls back to "no preview" so we never embed
// an unknown blob into the page.
function Preview({
  contentType,
  url,
  text,
}: {
  contentType: string;
  url: string | null;
  text: string | null;
}) {
  if (contentType.startsWith('image/') && url) {
    return <img src={url} alt="preview" className="max-w-full max-h-[600px] border border-ink-200" />;
  }
  if (contentType === 'application/pdf' && url) {
    return <iframe title="pdf" src={url} className="w-full h-[600px] border border-ink-200" />;
  }
  if (contentType.startsWith('video/') && url) {
    return <video src={url} controls className="max-w-full max-h-[600px]" />;
  }
  if (contentType.startsWith('audio/') && url) {
    return <audio src={url} controls className="w-full" />;
  }
  if (text !== null) {
    return (
      <pre className="text-xs border border-ink-200 p-3 max-h-[600px] overflow-auto whitespace-pre-wrap break-all">
        {text}
      </pre>
    );
  }
  return <p className="text-ink-500 text-sm">No inline preview for this type. Use Download.</p>;
}

function isPreviewable(ct: string): boolean {
  return (
    ct.startsWith('image/') ||
    ct.startsWith('video/') ||
    ct.startsWith('audio/') ||
    ct === 'application/pdf' ||
    isTextLike(ct)
  );
}

function isTextLike(ct: string): boolean {
  return (
    ct.startsWith('text/') ||
    ct === 'application/json' ||
    ct === 'application/xml' ||
    ct === 'application/javascript' ||
    ct === 'application/x-yaml' ||
    ct === 'application/yaml'
  );
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
