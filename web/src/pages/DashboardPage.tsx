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
  return n.toLocaleString('en-US');
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
        { label: 'Requests served', value: formatCount(Math.round(stats.requests)) },
      ]
    : [];

  return (
    <section>
      <h2 className="text-base mb-1">Dashboard</h2>
      <p className="text-xs text-ink-500 mb-6">Storage totals and lifetime request count.</p>

      {error && <ErrorBanner message={error} className="mb-4" />}

      {!stats ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <div className="grid grid-cols-2 gap-px bg-ink-200 border border-ink-200 max-w-xl md:grid-cols-4">
          {cards.map((c) => (
            <div key={c.label} className="bg-paper p-4">
              <div className="text-xs text-ink-500">{c.label}</div>
              <div className="text-xl mt-1 tabular-nums">{c.value}</div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
