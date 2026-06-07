import { useEffect, useState } from 'react';
import { getStats, type Stats } from '../lib/admin';
import { loadSession } from '../lib/session';
import { ErrorBanner } from '../components/ErrorBanner';

// formatBytes renders a byte count in the largest unit that keeps the number
// readable, mirroring how operators think about storage size.
function formatBytes(n: number): string {
  if (n < 1024) return `${Math.round(n)} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

function formatCount(n: number): string {
  return Math.round(n).toLocaleString('en-US');
}

export default function DashboardPage() {
  const session = loadSession();
  const [stats, setStats] = useState<Stats | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!session) return;
    getStats(session)
      .then(setStats)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
    // session is read once from localStorage; refetching on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const cards: ReadonlyArray<{ label: string; value: string }> = stats
    ? [
        { label: 'Buckets', value: formatCount(stats.buckets) },
        { label: 'Objects', value: formatCount(stats.objects) },
        { label: 'Storage used', value: formatBytes(stats.bytes) },
        { label: 'Multipart open', value: formatCount(stats.multipartInProgress) },
      ]
    : [];

  return (
    <section>
      <h2 className="text-base mb-1">Dashboard</h2>
      <p className="text-xs text-ink-500 mb-6">Storage footprint and object activity.</p>

      {error && <ErrorBanner message={error} className="mb-4" />}

      {!stats ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <div className="space-y-8 max-w-3xl">
          <div className="grid grid-cols-2 gap-px bg-ink-200 border border-ink-200 md:grid-cols-4">
            {cards.map((c) => (
              <div key={c.label} className="bg-paper p-4">
                <div className="text-xs text-ink-500">{c.label}</div>
                <div className="text-xl mt-1 tabular-nums">{c.value}</div>
              </div>
            ))}
          </div>

          <Activity stats={stats} />
          <PerBucket stats={stats} />
        </div>
      )}
    </section>
  );
}

function Activity({ stats }: Readonly<{ stats: Stats }>) {
  const a = stats.activity;
  const figures: ReadonlyArray<{ label: string; value: string }> = [
    { label: 'Uploads', value: formatCount(a.uploads) },
    { label: 'Downloads', value: formatCount(a.downloads) },
    { label: 'Deletes', value: formatCount(a.deletes) },
    { label: 'Data in', value: formatBytes(a.bytesIn) },
    { label: 'Data out', value: formatBytes(a.bytesOut) },
  ];
  return (
    <div>
      <h3 className="text-sm mb-2">Object activity (all buckets)</h3>
      <div className="flex flex-wrap gap-x-10 gap-y-3">
        {figures.map((f) => (
          <div key={f.label}>
            <div className="text-xs text-ink-500">{f.label}</div>
            <div className="text-lg tabular-nums">{f.value}</div>
          </div>
        ))}
      </div>
    </div>
  );
}

function PerBucket({ stats }: Readonly<{ stats: Stats }>) {
  if (!stats.perBucket || stats.perBucket.length === 0) {
    return <p className="text-ink-500 text-sm">No buckets yet.</p>;
  }
  return (
    <div>
      <h3 className="text-sm mb-2">Per bucket</h3>
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left border-b border-ink-200 text-ink-500">
            <th className="table-cell font-normal">Bucket</th>
            <th className="table-cell font-normal text-right w-28">Size</th>
            <th className="table-cell font-normal text-right w-24">Uploads</th>
            <th className="table-cell font-normal text-right w-24">Downloads</th>
            <th className="table-cell font-normal text-right w-24">Deletes</th>
          </tr>
        </thead>
        <tbody>
          {stats.perBucket.map((b) => (
            <tr key={b.name} className="border-b border-ink-100">
              <td className="table-cell font-mono text-xs break-all">{b.name}</td>
              <td className="table-cell text-xs text-ink-500 text-right tabular-nums">{formatBytes(b.bytes)}</td>
              <td className="table-cell text-xs text-right tabular-nums">{formatCount(b.uploads)}</td>
              <td className="table-cell text-xs text-right tabular-nums">{formatCount(b.downloads)}</td>
              <td className="table-cell text-xs text-right tabular-nums">{formatCount(b.deletes)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
