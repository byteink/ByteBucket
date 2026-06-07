import { useEffect, useState } from 'react';
import { getStats, type Stats } from '../lib/admin';
import { loadSession } from '../lib/session';
import { ErrorBanner } from '../components/ErrorBanner';

// formatBytes renders a byte count in the largest unit that keeps the number
// readable, mirroring how operators think about storage size.
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
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
        { label: 'Requests served', value: formatCount(stats.requests) },
      ]
    : [];

  return (
    <section>
      <h2 className="text-base mb-1">Dashboard</h2>
      <p className="text-xs text-ink-500 mb-6">Storage footprint and live service health.</p>

      {error && <ErrorBanner message={error} className="mb-4" />}

      {!stats ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <div className="space-y-8 max-w-2xl">
          <div className="grid grid-cols-2 gap-px bg-ink-200 border border-ink-200 md:grid-cols-4">
            {cards.map((c) => (
              <div key={c.label} className="bg-paper p-4">
                <div className="text-xs text-ink-500">{c.label}</div>
                <div className="text-xl mt-1 tabular-nums">{c.value}</div>
              </div>
            ))}
          </div>

          <RequestHealth stats={stats} />
          <TopBuckets stats={stats} />
        </div>
      )}
    </section>
  );
}

function RequestHealth({ stats }: Readonly<{ stats: Stats }>) {
  const sc = stats.statusClasses ?? {};
  const figures: ReadonlyArray<{ label: string; value: string; tone?: string }> = [
    { label: '2xx success', value: formatCount(sc['2xx'] ?? 0) },
    { label: '4xx client', value: formatCount(sc['4xx'] ?? 0), tone: (sc['4xx'] ?? 0) > 0 ? 'text-ink-900' : undefined },
    { label: '5xx server', value: formatCount(sc['5xx'] ?? 0), tone: (sc['5xx'] ?? 0) > 0 ? 'text-red-700' : undefined },
    { label: 'p95 latency', value: `${stats.p95LatencyMs.toFixed(1)} ms` },
    { label: 'Multipart open', value: formatCount(stats.multipartInProgress) },
  ];
  return (
    <div>
      <h3 className="text-sm mb-2">Request health</h3>
      <div className="flex flex-wrap gap-x-10 gap-y-3">
        {figures.map((f) => (
          <div key={f.label}>
            <div className="text-xs text-ink-500">{f.label}</div>
            <div className={`text-lg tabular-nums ${f.tone ?? ''}`}>{f.value}</div>
          </div>
        ))}
      </div>
    </div>
  );
}

function TopBuckets({ stats }: Readonly<{ stats: Stats }>) {
  if (!stats.topBuckets || stats.topBuckets.length === 0) {
    return null;
  }
  return (
    <div>
      <h3 className="text-sm mb-2">Largest buckets</h3>
      <table className="w-full max-w-md text-sm">
        <tbody>
          {stats.topBuckets.map((b) => (
            <tr key={b.name} className="border-b border-ink-100">
              <td className="table-cell font-mono text-xs break-all">{b.name}</td>
              <td className="table-cell text-xs text-ink-500 text-right tabular-nums w-28">
                {formatBytes(b.bytes)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
