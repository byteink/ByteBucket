import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import {
  deleteObject,
  downloadObject,
  getBucketACL,
  getConfig,
  getObjectMetadata,
  getObjectTagging,
  presignObject,
  publicObjectURL,
  putObjectACL,
  putObjectTagging,
  type CannedACL,
  type ObjectMetadata,
  type ObjectTag,
  type PresignedURL,
} from '../lib/s3';
import { loadSession, type Session } from '../lib/session';
import { errorMessage, formatBytes, formatCount, formatDateTime } from '../lib/format';
import { ErrorBanner } from '../components/ErrorBanner';
import { Icon } from '../components/icons';
import { ObjectPreview } from '../components/ObjectPreview';
import { Badge, ConfirmDialog, CopyButton, IconButton, Loading, PageHeader, Saved, Tip, Visibility } from '../components/ui';
import { buildCrumbs, objectListPath } from '../lib/paths';

interface ObjectState {
  meta: ObjectMetadata;
  bucketACL: CannedACL;
  publicBaseURL: string;
}

type DialogKind = 'publish' | 'delete' | null;

const MAX_TAGS = 10;
const TTL_OPTIONS = [
  { seconds: 300, label: '5 minutes' },
  { seconds: 900, label: '15 minutes' },
  { seconds: 3600, label: '1 hour' },
  { seconds: 86400, label: '1 day' },
  { seconds: 604800, label: '7 days' },
];

// Object ACL wins, otherwise the bucket ACL. `inherited` drives the hint.
function effectiveACL(state: ObjectState): { acl: CannedACL; inherited: boolean } {
  const own = (state.meta['acl'] ?? '').toLowerCase();
  if (own === 'public-read' || own === 'private') return { acl: own, inherited: false };
  return { acl: state.bucketACL, inherited: true };
}

function isoDate(ts: string | undefined): string {
  if (!ts) return '—';
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? ts : d.toISOString();
}

export default function ObjectDetailPage() {
  const params = useParams();
  const navigate = useNavigate();
  const bucket = params.name ?? '';
  // React Router stores the splat under params['*']; it carries slashes that
  // are part of the object key. Trim a stray leading slash so the wire-shape
  // key matches what other handlers store.
  const key = (params['*'] ?? '').replace(/^\/+/, '');
  const session = useMemo(() => loadSession(), []);
  const [state, setState] = useState<ObjectState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tags, setTags] = useState<ObjectTag[] | null>(null);
  const [dims, setDims] = useState<{ w: number; h: number } | null>(null);
  const [dialog, setDialog] = useState<DialogKind>(null);
  const [dialogBusy, setDialogBusy] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);

  useEffect(() => {
    if (!session || !bucket || !key) return;
    let cancelled = false;
    (async () => {
      const [meta, bucketACL, cfg, tagSet] = await Promise.all([
        getObjectMetadata(session, bucket, key),
        getBucketACL(session, bucket),
        getConfig(session),
        getObjectTagging(session, bucket, key),
      ]);
      if (cancelled) return;
      setState({ meta, bucketACL, publicBaseURL: cfg.publicBaseURL });
      setTags(tagSet);
    })().catch((e: unknown) => {
      if (!cancelled) setError(errorMessage(e));
    });
    return () => {
      cancelled = true;
    };
  }, [session, bucket, key]);

  const acl = state ? effectiveACL(state) : { acl: 'private' as CannedACL, inherited: true };
  const prefix = key.includes('/') ? key.slice(0, key.lastIndexOf('/') + 1) : '';
  const fileName = key.split('/').pop() ?? key;

  function onDownload() {
    if (session) downloadObject(session, bucket, key).catch((e: unknown) => setError(errorMessage(e)));
  }

  async function setACL(next: CannedACL) {
    if (!session || !state) return;
    await putObjectACL(session, bucket, key, next);
    const meta = await getObjectMetadata(session, bucket, key);
    setState({ ...state, meta });
  }

  function onToggleACL() {
    if (acl.acl === 'public-read') {
      setACL('private').catch((e: unknown) => setError(errorMessage(e)));
      return;
    }
    setDialogError(null);
    setDialog('publish');
  }

  async function runDialog(op: () => Promise<void>) {
    setDialogBusy(true);
    setDialogError(null);
    try {
      await op();
      setDialog(null);
    } catch (e) {
      setDialogError(errorMessage(e));
    } finally {
      setDialogBusy(false);
    }
  }

  async function removeObject() {
    if (!session) return;
    await deleteObject(session, bucket, key);
    navigate(objectListPath(bucket, prefix));
  }

  const sub = state
    ? [
        formatBytes(parseInt(state.meta['content-length'] ?? '0', 10)),
        state.meta['content-type'] ?? 'application/octet-stream',
        ...(dims ? [`${dims.w} × ${dims.h}`] : []),
        `modified ${formatDateTime(state.meta['last-modified'] ?? '')}`,
      ].join(' · ')
    : undefined;

  return (
    <section>
      <nav className="crumb" aria-label="Breadcrumb">
        <Link to="/buckets">Buckets</Link>
        <span>/</span>
        <Link to={objectListPath(bucket, '')} className="font-mono">
          {bucket}
        </Link>
        {buildCrumbs(prefix).map((c) => (
          <span key={c.prefix} className="contents">
            <span>/</span>
            <Link to={objectListPath(bucket, c.prefix)} className="font-mono">
              {c.label}
            </Link>
          </span>
        ))}
        <span>/</span>
        <span className="here">{fileName}</span>
      </nav>
      <PageHeader
        title={<span className="font-mono">{fileName}</span>}
        sub={sub}
        actions={
          <>
            <button type="button" className="btn" onClick={onDownload} disabled={!state}>
              <Icon name="download" />
              Download
            </button>
            <button type="button" className="btn" onClick={onToggleACL} disabled={!state}>
              <Icon name={acl.acl === 'public-read' ? 'lock' : 'globe'} />
              {acl.acl === 'public-read' ? 'Make private' : 'Make public'}
            </button>
            <button
              type="button"
              className="btn-danger"
              disabled={!state}
              onClick={() => {
                setDialogError(null);
                setDialog('delete');
              }}
            >
              <Icon name="trash" />
              Delete
            </button>
          </>
        }
      />

      {error && <ErrorBanner message={error} className="mb-4" />}

      {!state || !session ? (
        <Loading />
      ) : (
        <div className="grid grid-cols-[minmax(0,1fr)_400px] gap-10 items-start">
          <ObjectPreview
            session={session}
            bucket={bucket}
            objectKey={key}
            contentType={state.meta['content-type'] ?? ''}
            onDimensions={(w, h) => setDims({ w, h })}
            onError={setError}
          />
          <aside className="flex flex-col gap-7">
            <Details objectKey={key} meta={state.meta} acl={acl} />
            <Share
              session={session}
              bucket={bucket}
              objectKey={key}
              publicBaseURL={state.publicBaseURL}
              isPublic={acl.acl === 'public-read'}
              onError={setError}
            />
            {tags && <TagEditor session={session} bucket={bucket} objectKey={key} initial={tags} onError={setError} />}
          </aside>
        </div>
      )}

      <ConfirmDialog
        open={dialog === 'publish'}
        title={`Make ${key} public?`}
        body="Anyone with the URL will be able to download this object."
        confirmLabel="Make public"
        busy={dialogBusy}
        error={dialogError}
        onConfirm={() => void runDialog(() => setACL('public-read'))}
        onClose={() => setDialog(null)}
      />
      <ConfirmDialog
        open={dialog === 'delete'}
        title={`Delete ${key}?`}
        body="This cannot be undone."
        confirmLabel="Delete object"
        busy={dialogBusy}
        error={dialogError}
        onConfirm={() => void runDialog(removeObject)}
        onClose={() => setDialog(null)}
      />
    </section>
  );
}

function Details({
  objectKey,
  meta,
  acl,
}: Readonly<{ objectKey: string; meta: ObjectMetadata; acl: { acl: CannedACL; inherited: boolean } }>) {
  return (
    <section className="flex flex-col gap-2.5">
      <h2 className="text-sm font-medium">Details</h2>
      <dl className="dl">
        <dt>Key</dt>
        <dd>{objectKey}</dd>
        <dt>Size</dt>
        <dd>{formatCount(parseInt(meta['content-length'] ?? '0', 10))} bytes</dd>
        <dt>Content-Type</dt>
        <dd>{meta['content-type'] ?? 'application/octet-stream'}</dd>
        <dt>ETag</dt>
        <dd>{(meta['etag'] ?? '').replace(/"/g, '') || '—'}</dd>
        <dt>CRC32</dt>
        <dd>{meta['x-amz-checksum-crc32'] ?? '—'}</dd>
        <dt>Last-Modified</dt>
        <dd>{isoDate(meta['last-modified'])}</dd>
        <dt>Visibility</dt>
        <dd className="font-sans">
          {acl.acl === 'public-read' && acl.inherited ? (
            <Tip text="Inherited from the bucket ACL." pos="right">
              <Badge muted>Public</Badge>
            </Tip>
          ) : (
            <Visibility acl={acl.acl} source="object" />
          )}
        </dd>
      </dl>
    </section>
  );
}

function Share({
  session,
  bucket,
  objectKey,
  publicBaseURL,
  isPublic,
  onError,
}: Readonly<{
  session: Session;
  bucket: string;
  objectKey: string;
  publicBaseURL: string;
  isPublic: boolean;
  onError: (msg: string) => void;
}>) {
  const [ttl, setTTL] = useState(900);
  const [presigned, setPresigned] = useState<PresignedURL | null>(null);
  const [busy, setBusy] = useState(false);
  const shareURL = publicObjectURL(publicBaseURL, bucket, objectKey);

  async function onPresign() {
    setBusy(true);
    try {
      setPresigned(await presignObject(session, bucket, objectKey, ttl));
    } catch (e) {
      onError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="flex flex-col gap-2.5">
      <h2 className="text-sm font-medium">Share</h2>
      <div>
        <label className="field-label" htmlFor="public-url">
          Public URL
        </label>
        <div className="flex gap-1.5">
          <input id="public-url" className="input-mono" readOnly value={shareURL} onFocus={(e) => e.currentTarget.select()} />
          <CopyButton value={shareURL} label="Copy public URL" onError={onError} />
        </div>
        {!isPublic && <div className="hint">Object is private. Make it public, or use a presigned URL.</div>}
        {!publicBaseURL && (
          <div className="hint">
            Set <span className="font-mono">PUBLIC_BASE_URL</span> on the server to publish links under your own domain.
          </div>
        )}
      </div>
      <div>
        <label className="field-label" htmlFor="presign-ttl">
          Presigned URL
        </label>
        <div className="flex gap-1.5">
          <select id="presign-ttl" className="input w-[140px]" value={ttl} onChange={(e) => setTTL(Number(e.target.value))}>
            {TTL_OPTIONS.map((o) => (
              <option key={o.seconds} value={o.seconds}>
                {o.label}
              </option>
            ))}
          </select>
          <button type="button" className="btn" onClick={() => void onPresign()} disabled={busy}>
            {busy ? 'Working' : 'Generate'}
          </button>
        </div>
        {presigned ? (
          <div className="mt-1.5">
            <label className="sr-only" htmlFor="presigned-url">
              Presigned URL link
            </label>
            <div className="flex gap-1.5">
              <input
                id="presigned-url"
                className="input-mono"
                readOnly
                value={presigned.url}
                onFocus={(e) => e.currentTarget.select()}
              />
              <CopyButton value={presigned.url} label="Copy presigned URL" onError={onError} />
            </div>
            <div className="hint">Expires {formatDateTime(presigned.expiresAt)}. Works while the object is private.</div>
          </div>
        ) : (
          <div className="hint">Works while the object is private. Expires at the chosen time.</div>
        )}
      </div>
    </section>
  );
}

function sameTags(a: ObjectTag[], b: ObjectTag[]): boolean {
  return a.length === b.length && a.every((t, i) => t.key === b[i].key && t.value === b[i].value);
}

function TagEditor({
  session,
  bucket,
  objectKey,
  initial,
  onError,
}: Readonly<{ session: Session; bucket: string; objectKey: string; initial: ObjectTag[]; onError: (msg: string) => void }>) {
  const [saved, setSaved] = useState<ObjectTag[]>(initial);
  const [tags, setTags] = useState<ObjectTag[]>(initial);
  const [busy, setBusy] = useState(false);
  const [justSaved, setJustSaved] = useState(false);
  const dirty = !sameTags(tags, saved);

  function patch(i: number, p: Partial<ObjectTag>) {
    setJustSaved(false);
    setTags((ts) => ts.map((t, n) => (n === i ? { ...t, ...p } : t)));
  }

  async function onSave() {
    setBusy(true);
    try {
      // Drop blank-key rows so an empty editor line is not sent as a tag.
      const clean = tags.filter((t) => t.key.trim() !== '');
      await putObjectTagging(session, bucket, objectKey, clean);
      setSaved(clean);
      setTags(clean);
      setJustSaved(true);
    } catch (e) {
      onError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="flex flex-col gap-2.5">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium">
          Tags <span className="text-ink-500 font-normal">· {tags.length} of {MAX_TAGS}</span>
        </h2>
        <button
          type="button"
          className="btn-ghost btn-sm"
          disabled={tags.length >= MAX_TAGS}
          onClick={() => {
            setJustSaved(false);
            setTags((ts) => [...ts, { key: '', value: '' }]);
          }}
        >
          <Icon name="plus" />
          Add
        </button>
      </div>
      {tags.length === 0 ? (
        <p className="text-xs text-ink-500">No tags.</p>
      ) : (
        <div className="flex flex-col gap-1.5">
          {tags.map((t, i) => (
            <div key={i} className="flex gap-1.5 items-center">
              <label className="sr-only" htmlFor={`tag-key-${i}`}>
                Tag {i + 1} key
              </label>
              <input
                id={`tag-key-${i}`}
                className="input-mono flex-1"
                placeholder="key"
                value={t.key}
                onChange={(e) => patch(i, { key: e.target.value })}
              />
              <label className="sr-only" htmlFor={`tag-value-${i}`}>
                Tag {i + 1} value
              </label>
              <input
                id={`tag-value-${i}`}
                className="input-mono flex-1"
                placeholder="value"
                value={t.value}
                onChange={(e) => patch(i, { value: e.target.value })}
              />
              <IconButton
                icon="x"
                label="Remove tag"
                onClick={() => {
                  setJustSaved(false);
                  setTags((ts) => ts.filter((_, n) => n !== i));
                }}
              />
            </div>
          ))}
        </div>
      )}
      <div className="flex items-center gap-3">
        <button type="button" className="btn-primary" disabled={!dirty || busy} onClick={() => void onSave()}>
          {busy ? 'Saving' : 'Save tags'}
        </button>
        {justSaved && <Saved />}
      </div>
    </section>
  );
}
