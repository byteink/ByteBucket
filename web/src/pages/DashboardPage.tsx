import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { getStats, type Stats } from '../lib/admin';
import { loadSession } from '../lib/session';
import { errorMessage, formatBytes, formatCount } from '../lib/format';
import { ErrorBanner } from '../components/ErrorBanner';
import { RequestChart } from '../components/RequestChart';
import { Icon } from '../components/icons';
import { EmptyState, IconButton, Loading, PageHeader, Tip } from '../components/ui';

const MULTIPART_TIP = 'Multipart uploads started but not yet\ncompleted or aborted. Parts stay on disk.';
const TICK_MS = 5000;

// Re-renders every few seconds so "Updated Ns ago" keeps ticking without a
// refetch; the stats themselves only reload on demand.
function useNow(): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), TICK_MS);
    return () => clearInterval(t);
  }, []);
  return now;
}

export default function DashboardPage() {
  const session = loadSession();
  const [stats, setStats] = useState<Stats | null>(null);
  const [fetchedAt, setFetchedAt] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  const now = useNow();

  useEffect(() => {
    if (!session) return;
    let live = true;
    getStats(session)
      .then((s) => {
        if (!live) return;
        setStats(s);
        setFetchedAt(Date.now());
        setError(null);
      })
      .catch((e) => live && setError(errorMessage(e)));
    return () => {
      live = false;
    };
    // session is read once from localStorage; refetching on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reload]);

  const ago = fetchedAt === null ? null : Math.max(0, Math.round((now - fetchedAt) / 1000));

  return (
    <section>
      <PageHeader
        title="Overview"
        sub="Storage footprint and S3 traffic on this node."
        actions={
          <>
            {ago !== null && <span className="text-xs text-ink-500">Updated {ago}s ago</span>}
            <IconButton icon="refresh" label="Refresh now" onClick={() => setReload((n) => n + 1)} />
          </>
        }
      />

      {error && <ErrorBanner message={error} className="mb-4" />}

      {!stats ? (
        <Loading />
      ) : (
        <div className="flex flex-col gap-8">
          <Tiles stats={stats} />
          <RequestChart refreshKey={reload} />
          {stats.perBucket.length === 0 ? (
            <EmptyState
              text="No buckets yet."
              action={
                <Link to="/buckets" className="btn">
                  New bucket
                </Link>
              }
            />
          ) : (
            <>
              <Activity stats={stats} />
              <PerBucket rows={stats.perBucket} />
            </>
          )}
        </div>
      )}
    </section>
  );
}

function Tiles({ stats }: Readonly<{ stats: Stats }>) {
  return (
    <div className="stat-grid">
      <div className="stat">
        <div className="l">Buckets</div>
        <div className="v">{formatCount(stats.buckets)}</div>
      </div>
      <div className="stat">
        <div className="l">Objects</div>
        <div className="v">{formatCount(stats.objects)}</div>
      </div>
      <div className="stat">
        <div className="l">Storage used</div>
        <div className="v">{formatBytes(stats.bytes)}</div>
      </div>
      <div className="stat">
        <Tip text={MULTIPART_TIP} className="l">
          Open multipart <Icon name="info" size={13} />
        </Tip>
        <div className="v">{formatCount(stats.multipartInProgress)}</div>
      </div>
    </div>
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
    <section className="flex flex-col gap-3">
      <h2 className="text-sm font-medium">
        Object activity <span className="text-ink-500 font-normal">· all time</span>
      </h2>
      <div className="flex flex-wrap gap-x-8 gap-y-3">
        {figures.map((f) => (
          <div key={f.label}>
            <div className="text-xs text-ink-500">{f.label}</div>
            <div className="text-sm tabular-nums">{f.value}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

const NUM_COL = { width: 120 } as const;

function PerBucket({ rows }: Readonly<{ rows: Stats['perBucket'] }>) {
  const navigate = useNavigate();
  const objectsPath = (name: string) => `/buckets/${encodeURIComponent(name)}/objects`;
  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-sm font-medium">
        Per bucket <span className="text-ink-500 font-normal">· all time</span>
      </h2>
      <table className="tbl">
        <thead>
          <tr>
            <th>Bucket</th>
            <th className="num" style={NUM_COL}>Size</th>
            <th className="num" style={NUM_COL}>Objects</th>
            <th className="num" style={NUM_COL}>Uploads</th>
            <th className="num" style={NUM_COL}>Downloads</th>
            <th className="num" style={NUM_COL}>Deletes</th>
            <th style={{ width: 40 }} />
          </tr>
        </thead>
        <tbody>
          {rows.map((b) => (
            <tr key={b.name}>
              <td className="font-mono text-xs break-all">
                <Link className="hover:underline" to={objectsPath(b.name)}>
                  {b.name}
                </Link>
              </td>
              <td className="num text-ink-500">{formatBytes(b.bytes)}</td>
              <td className="num">{formatCount(b.objects)}</td>
              <td className="num">{formatCount(b.uploads)}</td>
              <td className="num">{formatCount(b.downloads)}</td>
              <td className="num">{formatCount(b.deletes)}</td>
              <td>
                <div className="acts">
                  <IconButton icon="open" label="Browse objects" onClick={() => navigate(objectsPath(b.name))} />
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
