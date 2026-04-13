import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  CreateBucketCommand,
  DeleteBucketCommand,
  ListBucketsCommand,
} from '@aws-sdk/client-s3';
import { loadSession } from '../lib/session';
import { makeS3Client } from '../lib/s3';

interface BucketRow {
  name: string;
  created?: string;
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
      const client = makeS3Client(session);
      const res = await client.send(new ListBucketsCommand({}));
      const list = (res.Buckets ?? []).map((b) => ({
        name: b.Name ?? '',
        created: b.CreationDate ? new Date(b.CreationDate).toISOString().slice(0, 10) : undefined,
      }));
      setBuckets(list);
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
      const client = makeS3Client(session);
      await client.send(new CreateBucketCommand({ Bucket: newName.trim() }));
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
      const client = makeS3Client(session);
      await client.send(new DeleteBucketCommand({ Bucket: name }));
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
              <th className="table-cell font-normal w-24"></th>
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
                <td className="table-cell text-xs text-ink-500">{b.created ?? '—'}</td>
                <td className="table-cell text-right">
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
