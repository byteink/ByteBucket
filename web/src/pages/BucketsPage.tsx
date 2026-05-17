import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  createBucket,
  deleteBucket,
  listBuckets,
  putBucketACL,
  type CannedACL,
} from '../lib/s3';
import { loadSession } from '../lib/session';

interface BucketRow {
  name: string;
  created?: string;
  acl: CannedACL;
}

export default function BucketsPage() {
  const session = loadSession();
  const [buckets, setBuckets] = useState<BucketRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [newName, setNewName] = useState('');

  async function refresh() {
    if (!session) return;
    setError(null);
    try {
      const list = await listBuckets(session);
      setBuckets(
        list.map((b) => ({
          name: b.name,
          created: b.creationDate
            ? new Date(b.creationDate).toISOString().slice(0, 10)
            : undefined,
          acl: b.acl ?? 'private',
        })),
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function onCreate() {
    if (!session || !newName.trim()) return;
    try {
      await createBucket(session, newName.trim());
      setNewName('');
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onDelete(name: string) {
    if (!session) return;
    if (!window.confirm(`Delete bucket ${name}? It must be empty.`)) return;
    try {
      await deleteBucket(session, name);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  // Promoting a bucket to public-read makes every non-overridden object
  // anonymously downloadable. We gate this behind a single confirm so an
  // accidental click cannot silently widen access to the whole bucket.
  async function onToggleACL(name: string, current: CannedACL) {
    if (!session) return;
    const next: CannedACL = current === 'public-read' ? 'private' : 'public-read';
    if (next === 'public-read') {
      const ok = window.confirm(
        `Make bucket "${name}" public?\n\nAnyone with the URL will be able to list and download every non-overridden object. Continue?`,
      );
      if (!ok) return;
    }
    try {
      await putBucketACL(session, name, next);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <section>
      <div className="flex items-baseline justify-between mb-6">
        <h2 className="text-base">Buckets</h2>
        <div className="flex gap-2">
          <input
            className="input w-56 font-mono"
            placeholder="new-bucket-name"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
          />
          <button className="btn-primary" onClick={onCreate} disabled={!newName.trim()}>
            Create
          </button>
        </div>
      </div>

      {error && <div className="text-xs text-ink-900 border-l-2 border-ink-900 pl-3 mb-4">{error}</div>}

      {buckets === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : buckets.length === 0 ? (
        <p className="text-ink-500 text-sm">No buckets yet.</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left border-b border-ink-200 text-ink-500">
              <th className="table-cell font-normal">Name</th>
              <th className="table-cell font-normal">Created</th>
              <th className="table-cell font-normal">Visibility</th>
              <th className="table-cell font-normal w-72"></th>
            </tr>
          </thead>
          <tbody>
            {buckets.map((b) => (
              <tr key={b.name} className="border-b border-ink-100">
                <td className="table-cell font-mono text-xs">
                  <Link className="hover:underline" to={`/buckets/${encodeURIComponent(b.name)}/objects`}>
                    {b.name}
                  </Link>
                </td>
                <td className="table-cell text-xs text-ink-500">{b.created ?? '-'}</td>
                <td className="table-cell text-xs">
                  {b.acl === 'public-read' ? (
                    <span className="inline-block px-2 py-0.5 border border-ink-900 text-ink-900 uppercase tracking-wide text-[10px]">
                      Public
                    </span>
                  ) : (
                    <span className="text-ink-500">Private</span>
                  )}
                </td>
                <td className="table-cell text-right">
                  <button
                    className="btn h-7 px-2 text-xs mr-2 inline-flex items-center"
                    onClick={() => onToggleACL(b.name, b.acl)}
                  >
                    {b.acl === 'public-read' ? 'Make private' : 'Make public'}
                  </button>
                  <Link
                    to={`/buckets/${encodeURIComponent(b.name)}/cors`}
                    className="btn h-7 px-2 text-xs mr-2 inline-flex items-center"
                  >
                    CORS
                  </Link>
                  <button className="btn-danger h-7 px-2 text-xs" onClick={() => onDelete(b.name)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
