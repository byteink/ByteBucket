import { useEffect, useState } from 'react';
import {
  getRequestSeries,
  type RequestBucket,
  type RequestRange,
  type RequestSeries,
} from '../lib/admin';
import { loadSession } from '../lib/session';
import { errorMessage, formatCount, formatDateTime } from '../lib/format';
import { ErrorBanner } from './ErrorBanner';
import { IconButton, Loading, Seg } from './ui';

// Selectable windows, narrowest to widest. Order drives the segmented control.
const RANGES: ReadonlyArray<{ key: RequestRange; label: string }> = [
  { key: '1h', label: '1h' },
  { key: '24h', label: '24h' },
  { key: '7d', label: '7d' },
  { key: '14d', label: '14d' },
  { key: '30d', label: '30d' },
];

type Class = 'c2xx' | 'c4xx' | 'c5xx';

// Rendered top-to-bottom so server errors sit on top and read first. Colour
// stays on-system: neutral inks for normal/client traffic, danger for 5xx.
const SEGMENTS: ReadonlyArray<{ key: Class; label: string; bg: string }> = [
  { key: 'c5xx', label: '5xx', bg: 'bg-danger' },
  { key: 'c4xx', label: '4xx', bg: 'bg-ink-500' },
  { key: 'c2xx', label: '2xx', bg: 'bg-ink-300' },
];

const DAY = 86400;
const X_TICKS = 6;

// Day-resolution windows would show a meaningless "00:00" on every label.
function stamp(unixSec: number, bucketSeconds: number): string {
  if (bucketSeconds < DAY) return formatDateTime(unixSec);
  return new Date(unixSec * 1000).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

// Axis labels trade precision for width: "1.2k" fits in the 40px gutter.
function compact(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return formatCount(n);
}

function total(b: RequestBucket): number {
  return b.c2xx + b.c4xx + b.c5xx;
}

export function RequestChart({ refreshKey = 0 }: Readonly<{ refreshKey?: number }>) {
  const session = loadSession();
  const [range, setRange] = useState<RequestRange>('24h');
  const [offset, setOffset] = useState(0);
  const [data, setData] = useState<RequestSeries | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!session) return;
    let live = true;
    getRequestSeries(session, range, offset)
      .then((d) => {
        if (!live) return;
        setData(d);
        setError(null);
      })
      .catch((e) => live && setError(errorMessage(e)));
    return () => {
      live = false;
    };
    // session is read once from localStorage; depending on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [range, offset, refreshKey]);

  function pickRange(r: RequestRange) {
    setRange(r);
    setOffset(0); // a new window always opens at "now"
  }

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center justify-between flex-wrap gap-4">
        <h2 className="text-sm font-medium">
          Requests <span className="text-ink-500 font-normal">· S3 API</span>
        </h2>
        <div className="flex items-center gap-3">
          <Seg options={RANGES} value={range} onChange={pickRange} label="Range" />
          <div className="flex items-center gap-1">
            <IconButton
              icon="left"
              label="Earlier window"
              disabled={!data?.canBack}
              onClick={() => setOffset((o) => o + 1)}
            />
            <span className="text-xs text-ink-500 tabular-nums min-w-[150px] text-center">
              {data ? `${stamp(data.from, data.bucketSeconds)} – ${stamp(data.to, data.bucketSeconds)}` : ' '}
            </span>
            <IconButton
              icon="right"
              label="Later window"
              disabled={!data?.canForward}
              onClick={() => setOffset((o) => Math.max(0, o - 1))}
            />
          </div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <Legend data={data} />

      {data ? <Plot data={data} /> : <Loading />}
    </section>
  );
}

function Legend({ data }: Readonly<{ data: RequestSeries | null }>) {
  return (
    <div className="flex gap-5 text-xs">
      {SEGMENTS.map((s) => (
        <span key={s.key} className="inline-flex items-center gap-1.5">
          <i className={`w-2.5 h-2.5 ${s.bg}`} />
          <span className="text-ink-500">{s.label}</span>
          <span className="text-[13px] tabular-nums">{data ? formatCount(data.totals[s.key]) : '—'}</span>
        </span>
      ))}
    </div>
  );
}

function Plot({ data }: Readonly<{ data: RequestSeries }>) {
  const max = Math.max(1, ...data.buckets.map(total));
  const step = Math.max(1, Math.floor(data.buckets.length / X_TICKS));
  const ticks = data.buckets.filter((_, i) => i % step === 0);
  return (
    <div className="grid gap-x-2" style={{ gridTemplateColumns: '40px minmax(0, 1fr)' }}>
      <div className="flex flex-col justify-between h-40 text-right text-xs text-ink-500 tabular-nums leading-none">
        <span>{compact(max)}</span>
        <span>{compact(max / 2)}</span>
        <span>0</span>
      </div>
      <div className="plot">
        {data.buckets.map((b) => (
          <Bar key={b.ts} bucket={b} max={max} bucketSeconds={data.bucketSeconds} />
        ))}
      </div>
      <div />
      <div className="flex justify-between pt-1.5 text-xs text-ink-500 tabular-nums">
        {ticks.map((b) => (
          <span key={b.ts}>{stamp(b.ts, data.bucketSeconds)}</span>
        ))}
      </div>
    </div>
  );
}

function Bar({
  bucket: b,
  max,
  bucketSeconds,
}: Readonly<{ bucket: RequestBucket; max: number; bucketSeconds: number }>) {
  const sum = total(b);
  const when = `${stamp(b.ts, bucketSeconds)} – ${stamp(b.ts + bucketSeconds, bucketSeconds)}`;
  const tip = `${when}\n2xx ${formatCount(b.c2xx)} · 4xx ${formatCount(b.c4xx)} · 5xx ${formatCount(b.c5xx)}`;
  return (
    <div className="bar">
      <div className="tt flex flex-col" data-tip={tip} style={{ height: `${(sum / max) * 100}%` }}>
        {SEGMENTS.map((s) =>
          b[s.key] > 0 ? (
            <div key={s.key} className={`${s.bg} mt-px`} style={{ height: `${(b[s.key] / sum) * 100}%` }} />
          ) : null,
        )}
      </div>
    </div>
  );
}
