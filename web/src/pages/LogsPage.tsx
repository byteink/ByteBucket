import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { getLogs, type LogCategory, type LogEvent } from '../lib/admin';
import { loadSession } from '../lib/session';
import { errorMessage, formatBytes, formatDateTime } from '../lib/format';
import { ErrorBanner } from '../components/ErrorBanner';
import { Dialog, EmptyState, IconButton, Loading, PageHeader, SearchInput, Seg } from '../components/ui';

const PAGE = 50;

const CATEGORIES = [
  { key: 'data', label: 'Access' },
  { key: 'control', label: 'Control' },
] as const;

const SUB: Record<LogCategory, string> = {
  data: 'Object access on the S3 port, newest first. Client IP follows the trusted-proxy setting.',
  control: 'Administrative actions: user and configuration changes, newest first.',
};

type StatusClass = '' | '2' | '3' | '4' | '5';

interface Filters {
  q: string;
  status: StatusClass;
  bucket: string;
  op: string;
}

const NO_FILTERS: Filters = { q: '', status: '', bucket: '', op: '' };

function isFiltering(f: Filters): boolean {
  return f.q !== '' || f.status !== '' || f.bucket !== '' || f.op !== '';
}

// Free-text search covers the columns an operator scans for: what was
// touched and by whom. The control log has no key or IP, so it searches the
// target and the action instead.
function searchText(e: LogEvent, category: LogCategory): string {
  const parts = category === 'data' ? [e.key, e.actor, e.clientIp] : [e.target, e.actor, e.op];
  return parts.filter(Boolean).join('\n').toLowerCase();
}

function matches(e: LogEvent, category: LogCategory, f: Filters): boolean {
  if (f.q !== '' && !searchText(e, category).includes(f.q.toLowerCase())) return false;
  if (f.status !== '' && Math.floor((e.status ?? 0) / 100) !== Number(f.status)) return false;
  if (f.bucket !== '' && e.bucket !== f.bucket) return false;
  if (f.op !== '' && e.op !== f.op) return false;
  return true;
}

function distinct(events: LogEvent[], pick: (e: LogEvent) => string | undefined): string[] {
  const seen = new Set<string>();
  for (const e of events) {
    const v = pick(e);
    if (v) seen.add(v);
  }
  return [...seen].sort();
}

function dash(v?: string): string {
  return v || '—';
}

// The server records -1 when a response size is unknown (chunked/no body).
function bytes(e: LogEvent): string {
  const n = (e.bytesOut ?? 0) > 0 ? e.bytesOut : e.bytesIn;
  return n === undefined || n < 0 ? '—' : formatBytes(n);
}

function duration(e: LogEvent): string {
  return `${e.durationMs ?? 0} ms`;
}

function FilterSelect({
  id,
  label,
  value,
  options,
  none,
  className,
  onChange,
}: Readonly<{
  id: string;
  label: string;
  value: string;
  options: ReadonlyArray<{ value: string; label: string }>;
  none: string;
  className: string;
  onChange: (v: string) => void;
}>) {
  return (
    <>
      <label className="sr-only" htmlFor={id}>
        {label}
      </label>
      <select id={id} className={`input ${className}`} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">{none}</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </>
  );
}

const STATUS_OPTIONS = ['2', '3', '4', '5'].map((c) => ({ value: c, label: `${c}xx` }));

function FilterBar({
  category,
  events,
  filters,
  shown,
  onChange,
}: Readonly<{
  category: LogCategory;
  events: LogEvent[];
  filters: Filters;
  shown: number;
  onChange: (f: Filters) => void;
}>) {
  const patch = (p: Partial<Filters>) => onChange({ ...filters, ...p });
  const asOptions = (vs: string[]) => vs.map((v) => ({ value: v, label: v }));
  return (
    <div className="flex items-center gap-2 mb-4">
      <SearchInput
        className="w-[280px]"
        value={filters.q}
        onChange={(q) => patch({ q })}
        placeholder={category === 'data' ? 'Filter by key, actor or IP' : 'Filter by target, actor or action'}
        label={category === 'data' ? 'Filter by key, actor or IP' : 'Filter by target, actor or action'}
      />
      {category === 'data' && (
        <>
          <FilterSelect
            id="log-status"
            label="Status"
            className="w-[130px]"
            value={filters.status}
            none="Any status"
            options={STATUS_OPTIONS}
            onChange={(v) => patch({ status: v as StatusClass })}
          />
          <FilterSelect
            id="log-bucket"
            label="Bucket"
            className="w-[150px]"
            value={filters.bucket}
            none="All buckets"
            options={asOptions(distinct(events, (e) => e.bucket))}
            onChange={(bucket) => patch({ bucket })}
          />
          <FilterSelect
            id="log-op"
            label="Operation"
            className="w-[150px]"
            value={filters.op}
            none="All operations"
            options={asOptions(distinct(events, (e) => e.op))}
            onChange={(op) => patch({ op })}
          />
        </>
      )}
      {isFiltering(filters) && (
        <span className="text-xs text-ink-500 ml-auto">
          Filtering {shown} of {events.length} loaded events
        </span>
      )}
    </div>
  );
}

function StatusCell({ e }: Readonly<{ e: LogEvent }>) {
  const status = e.status ?? 0;
  const tone = status >= 400 ? 'text-danger' : 'text-ink-500';
  if (!e.errorCode) return <td className={`font-mono tabular-nums ${tone}`}>{status || '—'}</td>;
  return (
    <td className={`font-mono tabular-nums ${tone}`}>
      <span className="tt border-b border-dotted border-current cursor-default" data-tip={e.errorCode}>
        {status}
      </span>
    </td>
  );
}

function AccessTable({ events, onDetails }: Readonly<{ events: LogEvent[]; onDetails: (e: LogEvent) => void }>) {
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>Time</th>
          <th>Status</th>
          <th>Operation</th>
          <th>Bucket</th>
          <th>Key</th>
          <th>Actor</th>
          <th>Client IP</th>
          <th className="num">Bytes</th>
          <th className="num">Duration</th>
          <th className="w-8" />
        </tr>
      </thead>
      <tbody>
        {events.map((e) => (
          <tr key={e.ts}>
            <td className="font-mono text-ink-500 whitespace-nowrap">{formatDateTime(e.time, true)}</td>
            <StatusCell e={e} />
            <td>{e.op}</td>
            <td className="font-mono text-ink-500">{dash(e.bucket)}</td>
            <td className="font-mono break-all">{dash(e.key)}</td>
            <td className="font-mono text-ink-500">{e.actor || 'anonymous'}</td>
            <td className="font-mono text-ink-500">{dash(e.clientIp)}</td>
            <td className="num text-ink-500 text-xs whitespace-nowrap">{bytes(e)}</td>
            <td className="num text-ink-500 text-xs whitespace-nowrap">{duration(e)}</td>
            <td>
              <div className="acts">
                <IconButton icon="info" label="Request details" onClick={() => onDetails(e)} />
              </div>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function ControlTable({ events, onDetails }: Readonly<{ events: LogEvent[]; onDetails: (e: LogEvent) => void }>) {
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>Time</th>
          <th>Action</th>
          <th>Target</th>
          <th>Actor</th>
          <th>Details</th>
          <th className="w-8" />
        </tr>
      </thead>
      <tbody>
        {events.map((e) => (
          <tr key={e.ts}>
            <td className="font-mono text-ink-500 whitespace-nowrap">{formatDateTime(e.time, true)}</td>
            <td>{e.op}</td>
            <td className="font-mono break-all">{dash(e.target)}</td>
            <td className="font-mono text-ink-500">{dash(e.actor)}</td>
            <td className="text-ink-500 text-xs max-w-md truncate">{dash(e.detail)}</td>
            <td>
              <div className="acts">
                <IconButton icon="info" label="Request details" onClick={() => onDetails(e)} />
              </div>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function detailRows(e: LogEvent, category: LogCategory): Array<[string, string]> {
  const head: Array<[string, string]> = [
    ['Time', e.time],
    ['Operation', e.op],
    ['Actor', e.actor || 'anonymous'],
  ];
  if (category === 'control') {
    return [...head, ['Target', dash(e.target)], ['Detail', dash(e.detail)]];
  }
  return [
    ...head,
    ['Status', String(e.status ?? '—')],
    ['Error code', dash(e.errorCode)],
    ['Bucket', dash(e.bucket)],
    ['Key', dash(e.key)],
    ['Client IP', dash(e.clientIp)],
    ['Bytes in', formatBytes(e.bytesIn ?? 0)],
    ['Bytes out', formatBytes(e.bytesOut ?? 0)],
    ['Duration', duration(e)],
    ['User agent', dash(e.userAgent)],
    ['Detail', dash(e.detail)],
  ];
}

function DetailsDialog({
  event,
  category,
  onClose,
}: Readonly<{ event: LogEvent | null; category: LogCategory; onClose: () => void }>) {
  return (
    <Dialog open={event !== null} title="Request details" onClose={onClose}>
      {event && (
        <dl className="dl mt-3">
          {detailRows(event, category).map(([k, v]) => (
            <div key={k} className="contents">
              <dt>{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      )}
      <div className="btns">
        <button type="button" className="btn" onClick={onClose}>
          Close
        </button>
      </div>
    </Dialog>
  );
}

function Footer({
  count,
  exhausted,
  busy,
  onMore,
}: Readonly<{ count: number; exhausted: boolean; busy: boolean; onMore: () => void }>) {
  return (
    <div className="flex items-center gap-4 mt-3 text-xs text-ink-500">
      {!exhausted && (
        <button type="button" className="btn btn-sm" disabled={busy} onClick={onMore}>
          {busy ? 'Loading' : 'Load older'}
        </button>
      )}
      <span>{count} events loaded</span>
    </div>
  );
}

function Empty({ category }: Readonly<{ category: LogCategory }>) {
  if (category === 'control') return <EmptyState text="No control events." />;
  return (
    <EmptyState
      text="No access events. Enable access logging in Settings."
      action={
        <Link to="/settings" className="btn">
          Open Settings
        </Link>
      }
    />
  );
}

export default function LogsPage() {
  const session = loadSession();
  const [category, setCategory] = useState<LogCategory>('data');
  const [events, setEvents] = useState<LogEvent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [exhausted, setExhausted] = useState(false);
  const [filters, setFilters] = useState<Filters>(NO_FILTERS);
  const [details, setDetails] = useState<LogEvent | null>(null);

  useEffect(() => {
    if (!session) return;
    let live = true;
    setEvents(null);
    setExhausted(false);
    setError(null);
    setFilters(NO_FILTERS);
    getLogs(session, category, PAGE)
      .then((e) => {
        if (!live) return;
        setEvents(e);
        setExhausted(e.length < PAGE);
      })
      .catch((e) => live && setError(errorMessage(e)));
    return () => {
      live = false;
    };
    // session is read once from localStorage; depending on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [category]);

  async function loadMore() {
    if (!session || !events || events.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const older = await getLogs(session, category, PAGE, events[events.length - 1].ts);
      setEvents([...events, ...older]);
      if (older.length < PAGE) setExhausted(true);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const shown = events?.filter((e) => matches(e, category, filters)) ?? [];

  return (
    <section>
      <PageHeader
        title="Logs"
        sub={SUB[category]}
        actions={<Seg options={CATEGORIES} value={category} onChange={setCategory} label="Log category" />}
      />

      {error && <ErrorBanner message={error} className="mb-4" />}

      {events === null ? (
        <Loading />
      ) : events.length === 0 ? (
        <Empty category={category} />
      ) : (
        <>
          <FilterBar category={category} events={events} filters={filters} shown={shown.length} onChange={setFilters} />
          {category === 'data' ? (
            <AccessTable events={shown} onDetails={setDetails} />
          ) : (
            <ControlTable events={shown} onDetails={setDetails} />
          )}
          <Footer count={events.length} exhausted={exhausted} busy={busy} onMore={loadMore} />
        </>
      )}

      <DetailsDialog event={details} category={category} onClose={() => setDetails(null)} />
    </section>
  );
}
