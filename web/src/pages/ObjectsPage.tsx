import { ChangeEvent, DragEvent, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { deleteObject, getObject, listObjects, putObject } from '../lib/s3';
import { loadSession } from '../lib/session';

interface ObjectRow {
  key: string;
  size: number;
  modified?: string;
}

export default function ObjectsPage() {
  const { name } = useParams<{ name: string }>();
  const bucket = name ?? '';
  const session = loadSession();
  const [rows, setRows] = useState<ObjectRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dragOver, setDragOver] = useState(false);

  async function refresh() {
    if (!session || !bucket) return;
    setError(null);
    try {
      const list = await listObjects(session, bucket);
      setRows(
        list.map((o) => ({
          key: o.key,
          size: o.size,
          modified: o.lastModified ? new Date(o.lastModified).toISOString() : undefined,
        })),
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bucket]);

  async function uploadFiles(files: FileList | File[]) {
    if (!session || !bucket) return;
    try {
      for (const file of Array.from(files)) {
        await putObject(session, bucket, file.name, file);
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
      // Trigger a download via an ephemeral object URL. Revoke immediately
      // after click so the blob can be GC'd; browsers keep the download alive
      // through the Blob reference held by the download dialog.
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

  return (
    <section>
      <nav className="text-xs text-ink-500 mb-2">
        <Link to="/buckets" className="hover:underline">Buckets</Link>
        <span className="mx-1">/</span>
        <span className="font-mono text-ink-900">{bucket}</span>
      </nav>
      <div className="flex items-baseline justify-between mb-6">
        <h2 className="text-base">Objects</h2>
        <label className="btn-primary cursor-pointer">
          Upload
          <input type="file" className="hidden" multiple onChange={onInput} />
        </label>
      </div>

      <div
        onDragEnter={() => setDragOver(true)}
        onDragLeave={() => setDragOver(false)}
        onDragOver={(e) => e.preventDefault()}
        onDrop={onDrop}
        className={`border border-dashed ${dragOver ? 'border-ink-900' : 'border-ink-200'} p-6 mb-6 text-center text-xs text-ink-500`}
      >
        Drop files here to upload
      </div>

      {error && <div className="text-xs text-ink-900 border-l-2 border-ink-900 pl-3 mb-4">{error}</div>}

      {rows === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : rows.length === 0 ? (
        <p className="text-ink-500 text-sm">Empty bucket.</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left border-b border-ink-200 text-ink-500">
              <th className="table-cell font-normal">Key</th>
              <th className="table-cell font-normal w-24">Size</th>
              <th className="table-cell font-normal w-56">Modified</th>
              <th className="table-cell font-normal w-44"></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((o) => (
              <tr key={o.key} className="border-b border-ink-100">
                <td className="table-cell font-mono text-xs break-all">{o.key}</td>
                <td className="table-cell text-xs text-ink-500">{formatSize(o.size)}</td>
                <td className="table-cell text-xs text-ink-500">{o.modified ?? '-'}</td>
                <td className="table-cell text-right">
                  <button className="btn h-7 px-2 text-xs mr-2" onClick={() => onDownload(o.key)}>
                    Download
                  </button>
                  <button className="btn-danger h-7 px-2 text-xs" onClick={() => onDelete(o.key)}>
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

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
