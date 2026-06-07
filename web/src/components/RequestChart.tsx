import { useEffect, useState } from 'react';
import {
  getRequestSeries,
  type RequestRange,
  type RequestSeries,
} from '../lib/admin';
import { loadSession } from '../lib/session';
import { ErrorBanner } from './ErrorBanner';

// Selectable windows, narrowest to widest. Order drives the segmented control.
const RANGES: ReadonlyArray<{ key: RequestRange; label: string }> = [
  { key: '1h', label: '1h' },
  { key: '24h', label: '24h' },
  { key: '7d', label: '7d' },
  { key: '14d', label: '14d' },
  { key: '30d', label: '30d' },
];

function fmt(unixSec: number, withTime: boolean): string {
  const opts: Intl.DateTimeFormatOptions = withTime
    ? { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }
    : { month: 'short', day: 'numeric' };
  return new Date(unixSec * 1000).toLocaleString(undefined, opts);
}

function compact(n: number): string {
  return n.toLocaleString('en-US');
}

// Three classes, rendered bottom-to-top so server errors sit on top and read
// first. Colour stays on-system: neutral inks for normal/client traffic, the
// single danger token for 5xx.
const SEGMENTS: ReadonlyArray<{ key: 'c2xx' | 'c4xx' | 'c5xx'; label: string; bar: string; dot: string }> = [
  { key: 'c5xx', label: '5xx', bar: 'bg-danger', dot: 'bg-danger' },
  { key: 'c4xx', label: '4xx', bar: 'bg-ink-500', dot: 'bg-ink-500' },
  { key: 'c2xx', label: '2xx', bar: 'bg-ink-300', dot: 'bg-ink-300' },
];

export function RequestChart() {
  const session = loadSession();
  const [range, setRange] = useState<RequestRange>('24h');
  const [offset, setOffset] = useState(0);
  const [data, setData] = useState<RequestSeries | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!session) return;
    let live = true;
    getRequestSeries(session, range, offset)
      .then((d) => live && setData(d))
      .catch((e) => live && setError(e instanceof Error ? e.message : String(e)));
    return () => {
      live = false;
    };
    // session is read once from localStorage; depending on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [range, offset]);

  function pickRange(r: RequestRange) {
    setRange(r);
    setOffset(0); // a new window always opens at "now"
  }

  const withTime = data ? data.bucketSeconds < 86400 : false;
  const maxTotal = data
    ? Math.max(1, ...data.buckets.map((b) => b.c2xx + b.c4xx + b.c5xx))
    : 1;

  return (
    <div>
      <div className="flex items-center justify-between flex-wrap gap-2 mb-3">
        <h3 className="text-sm">Request outcomes (S3 API)</h3>
        <div className="flex items-center gap-px">
          {RANGES.map((r) => (
            <button
              key={r.key}
              type="button"
              onClick={() => pickRange(r.key)}
              className={`h-7 px-2 text-xs border border-ink-200 ${
                r.key === range ? 'bg-ink-900 text-ink-0' : 'bg-ink-0 text-ink-700 hover:bg-ink-50'
              }`}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>

      {error && <ErrorBanner message={error} className="mb-3" />}

      <div className="flex items-center justify-between mb-3">
        <div className="flex gap-5">
          {SEGMENTS.map((s) => (
            <div key={s.key} className="flex items-center gap-1.5">
              <span className={`inline-block w-2.5 h-2.5 ${s.dot}`} />
              <span className="text-xs text-ink-500">{s.label}</span>
              <span className="text-sm tabular-nums">
                {data ? compact(data.totals[s.key]) : '—'}
              </span>
            </div>
          ))}
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="btn h-7 px-2 text-xs"
            disabled={!data?.canBack}
            onClick={() => setOffset((o) => o + 1)}
            aria-label="Earlier window"
          >
            ←
          </button>
          <span className="text-xs text-ink-500 tabular-nums min-w-[11rem] text-center">
            {data ? `${fmt(data.from, withTime)} – ${fmt(data.to, withTime)}` : ' '}
          </span>
          <button
            type="button"
            className="btn h-7 px-2 text-xs"
            disabled={!data?.canForward}
            onClick={() => setOffset((o) => Math.max(0, o - 1))}
            aria-label="Later window"
          >
            →
          </button>
        </div>
      </div>

      {!data ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : (
        <div className="flex items-stretch gap-px h-40 border-b border-ink-200">
          {data.buckets.map((b) => {
            const total = b.c2xx + b.c4xx + b.c5xx;
            const barPct = (total / maxTotal) * 100;
            const title = `${fmt(b.ts, true)} · 2xx ${b.c2xx} · 4xx ${b.c4xx} · 5xx ${b.c5xx}`;
            return (
              <div key={b.ts} className="flex-1 min-w-0 flex flex-col justify-end" title={title}>
                <div className="flex flex-col" style={{ height: `${barPct}%` }}>
                  {SEGMENTS.map((s) =>
                    b[s.key] > 0 ? (
                      <div
                        key={s.key}
                        className={s.bar}
                        style={{ height: `${(b[s.key] / total) * 100}%` }}
                      />
                    ) : null,
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
