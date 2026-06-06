import { ChangeEvent, DragEvent, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import {
  copyObject,
  deleteObject,
  deleteObjects,
  getObject,
  putObject,
  putObjectACL,
  type CannedACL,
} from '../lib/s3';
import { loadSession } from '../lib/session';
import { useObjectListing, type ObjectRow } from '../lib/useObjectListing';
import { ErrorBanner } from '../components/ErrorBanner';

export default function ObjectsPage() {
  const { name } = useParams<{ name: string }>();
  const bucket = name ?? '';
  const [params, setParams] = useSearchParams();
  // Current folder, always either "" (bucket root) or ends with "/".
  const prefix = params.get('prefix') ?? '';
  const session = loadSession();
  const { rows, folders, bucketACL, error, loadingMore, hasMore, refresh, loadMore, setError } =
    useObjectListing(session, bucket, prefix);
  const [dragOver, setDragOver] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  function toggleSelected(key: string) {
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  async function onDeleteSelected() {
    if (!session || !bucket || selected.size === 0) return;
    if (!window.confirm(`Delete ${selected.size} selected object(s)?`)) return;
    try {
      const failures = await deleteObjects(session, bucket, Array.from(selected));
      setSelected(new Set());
      await refresh();
      if (failures.length > 0) {
        setError(`${failures.length} object(s) could not be deleted: ${failures.map((f) => f.key).join(', ')}`);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  function navigate(nextPrefix: string) {
    const next = new URLSearchParams(params);
    if (nextPrefix) next.set('prefix', nextPrefix);
    else next.delete('prefix');
    setParams(next);
  }

  async function uploadFiles(files: FileList | File[]) {
    if (!session || !bucket) return;
    try {
      for (const file of Array.from(files)) {
        await putObject(session, bucket, prefix + file.name, file);
      }
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onDownload(key: string) {
    if (!session || !bucket) return;
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

  async function onDelete(key: string) {
    if (!session || !bucket) return;
    if (!window.confirm(`Delete ${key}?`)) return;
    try {
      await deleteObject(session, bucket, key);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onRename(key: string) {
    if (!session || !bucket) return;
    const dst = window.prompt('Move/rename to (full key within bucket):', key);
    if (!dst || dst === key) return;
    try {
      // Copy then delete the source: the closest S3 has to an atomic rename.
      await copyObject(session, bucket, key, dst);
      await deleteObject(session, bucket, key);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onToggleACL(o: ObjectRow) {
    if (!session || !bucket) return;
    const next: CannedACL = o.acl === 'public-read' ? 'private' : 'public-read';
    if (next === 'public-read') {
      const ok = window.confirm(
        `Make "${o.key}" public?\n\nAnyone with the URL will be able to download this object. Continue?`,
      );
      if (!ok) return;
    }
    try {
      await putObjectACL(session, bucket, o.key, next);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  function onDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    setDragOver(false);
    if (e.dataTransfer.files.length > 0) {
      void uploadFiles(e.dataTransfer.files);
    }
  }

  function onInput(e: ChangeEvent<HTMLInputElement>) {
    if (e.target.files && e.target.files.length > 0) {
      void uploadFiles(e.target.files);
      e.target.value = '';
    }
  }

  const crumbs = buildCrumbs(prefix);

  return (
    <section>
      <nav className="text-xs text-ink-500 mb-2">
        <Link to="/buckets" className="hover:underline">Buckets</Link>
        <span className="mx-1">/</span>
        <button
          type="button"
          className="font-mono text-ink-900 hover:underline"
          onClick={() => navigate('')}
        >
          {bucket}
        </button>
        {crumbs.map((c) => (
          <span key={c.prefix}>
            <span className="mx-1">/</span>
            <button
              type="button"
              className="font-mono text-ink-900 hover:underline"
              onClick={() => navigate(c.prefix)}
            >
              {c.label}
            </button>
          </span>
        ))}
      </nav>
      <div className="flex items-baseline justify-between mb-6">
        <h2 className="text-base">
          Objects
          {bucketACL === 'public-read' && (
            <span className="ml-3 inline-block px-2 py-0.5 border border-ink-900 text-ink-900 uppercase tracking-wide text-[10px] align-middle">
              Public bucket
            </span>
          )}
        </h2>
        <label className="btn-primary cursor-pointer">
          Upload
          <input type="file" className="hidden" multiple onChange={onInput} />
        </label>
      </div>

      {bucketACL === 'public-read' && (
        <div className="text-xs text-ink-900 border border-ink-900 px-3 py-2 mb-4">
          This bucket is public-read. All objects are anonymously readable unless individually overridden to private.
        </div>
      )}

      <div
        onDragEnter={() => setDragOver(true)}
        onDragLeave={() => setDragOver(false)}
        onDragOver={(e) => e.preventDefault()}
        onDrop={onDrop}
        className={`border border-dashed ${dragOver ? 'border-ink-900' : 'border-ink-200'} p-6 mb-6 text-center text-xs text-ink-500`}
      >
        Drop files here to upload{prefix ? ` into ${prefix}` : ''}
      </div>

      {error && <ErrorBanner message={error} className="mb-4" />}

      {rows === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : rows.length === 0 && folders.length === 0 ? (
        <p className="text-ink-500 text-sm">{prefix ? 'Empty folder.' : 'Empty bucket.'}</p>
      ) : (
        <>
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left border-b border-ink-200 text-ink-500">
              <th className="table-cell font-normal w-8"></th>
              <th className="table-cell font-normal">Name</th>
              <th className="table-cell font-normal w-24">Size</th>
              <th className="table-cell font-normal w-56">Modified</th>
              <th className="table-cell font-normal w-44">Visibility</th>
              <th className="table-cell font-normal w-56"></th>
            </tr>
          </thead>
          <tbody>
            {folders.map((p) => (
              <tr key={p} className="border-b border-ink-100">
                <td className="table-cell"></td>
                <td className="table-cell font-mono text-xs break-all">
                  <button
                    type="button"
                    className="hover:underline text-left"
                    onClick={() => navigate(p)}
                  >
                    {p.slice(prefix.length)}
                  </button>
                </td>
                <td className="table-cell text-xs text-ink-500">-</td>
                <td className="table-cell text-xs text-ink-500">-</td>
                <td className="table-cell text-xs text-ink-500">-</td>
                <td className="table-cell"></td>
              </tr>
            ))}
            {rows.map((o) => (
              <tr key={o.key} className="border-b border-ink-100">
                <td className="table-cell">
                  <input
                    type="checkbox"
                    aria-label={`Select ${o.key}`}
                    checked={selected.has(o.key)}
                    onChange={() => toggleSelected(o.key)}
                  />
                </td>
                <td className="table-cell font-mono text-xs break-all">
                  <Link
                    className="hover:underline"
                    to={`/buckets/${encodeURIComponent(bucket)}/objects/${o.key
                      .split('/')
                      .map((p) => encodeURIComponent(p))
                      .join('/')}`}
                  >
                    {o.key.slice(prefix.length)}
                  </Link>
                </td>
                <td className="table-cell text-xs text-ink-500">{formatSize(o.size)}</td>
                <td className="table-cell text-xs text-ink-500">{o.modified ?? '-'}</td>
                <td className="table-cell text-xs">{renderVisibility(o.acl, o.aclSource)}</td>
                <td className="table-cell text-right">
                  <button
                    className="btn h-7 px-2 text-xs mr-2"
                    onClick={() => onToggleACL(o)}
                  >
                    {o.acl === 'public-read' ? 'Make private' : 'Make public'}
                  </button>
                  <button className="btn h-7 px-2 text-xs mr-2" onClick={() => onDownload(o.key)}>
                    Download
                  </button>
                  <button className="btn h-7 px-2 text-xs mr-2" onClick={() => void onRename(o.key)}>
                    Rename
                  </button>
                  <button className="btn-danger h-7 px-2 text-xs" onClick={() => onDelete(o.key)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <div className="mt-4 flex items-center gap-4 border-t border-ink-100 pt-3 text-xs text-ink-500">
          {selected.size > 0 && (
            <button type="button" className="btn-danger h-8 px-3 text-xs" onClick={() => void onDeleteSelected()}>
              Delete selected ({selected.size})
            </button>
          )}
          <span>
            Showing {folders.length} folder{folders.length === 1 ? '' : 's'} and {rows.length} object
            {rows.length === 1 ? '' : 's'}
            {hasMore && <span className="text-ink-900"> — more not loaded yet</span>}
          </span>
          {hasMore && (
            <button
              type="button"
              className="btn-primary h-8 px-4 text-xs"
              onClick={() => void loadMore()}
              disabled={loadingMore}
            >
              {loadingMore ? 'Loading.' : 'Load more'}
            </button>
          )}
        </div>
        </>
      )}
    </section>
  );
}

// buildCrumbs turns "a/b/c/" into [{label:"a", prefix:"a/"}, {label:"b", prefix:"a/b/"}, ...]
// so each segment is independently clickable.
function buildCrumbs(prefix: string): { label: string; prefix: string }[] {
  if (!prefix) return [];
  const parts = prefix.split('/').filter((p) => p.length > 0);
  const out: { label: string; prefix: string }[] = [];
  let acc = '';
  for (const p of parts) {
    acc += p + '/';
    out.push({ label: p, prefix: acc });
  }
  return out;
}

function renderVisibility(acl: CannedACL, source: 'object' | 'bucket' | 'default') {
  if (acl === 'public-read') {
    return (
      <span className="inline-block px-2 py-0.5 border border-ink-900 text-ink-900 uppercase tracking-wide text-[10px]">
        {source === 'bucket' ? 'Public (inherited)' : 'Public'}
      </span>
    );
  }
  return <span className="text-ink-500">Private</span>;
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
