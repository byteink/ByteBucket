import { useEffect, useState } from 'react';
import { getLogs, type LogEvent, type LogCategory } from '../lib/admin';
import { loadSession } from '../lib/session';
import { ErrorBanner } from '../components/ErrorBanner';

const PAGE = 50;

function fmtTime(rfc: string): string {
  const d = new Date(rfc);
  if (Number.isNaN(d.getTime())) return rfc;
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

function tabClass(active: boolean): string {
  return `h-8 px-3 text-xs border ${
    active ? 'border-ink-900 text-ink-900' : 'border-ink-200 text-ink-500 hover:text-ink-900'
  }`;
}

function DataTable({ events }: { events: LogEvent[] }) {
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left border-b border-ink-200 text-ink-500">
          <th className="table-cell font-normal w-44">Time</th>
          <th className="table-cell font-normal">Operation</th>
          <th className="table-cell font-normal">Bucket</th>
          <th className="table-cell font-normal">Key</th>
          <th className="table-cell font-normal">Actor</th>
          <th className="table-cell font-normal w-16">Status</th>
        </tr>
      </thead>
      <tbody>
        {events.map((e) => (
          <tr key={e.ts} className="border-b border-ink-100">
            <td className="table-cell text-ink-500 text-xs font-mono">{fmtTime(e.time)}</td>
            <td className="table-cell text-xs">{e.op}</td>
            <td className="table-cell text-ink-500 text-xs font-mono break-all">{e.bucket || '—'}</td>
            <td className="table-cell text-ink-500 text-xs font-mono break-all">{e.key || '—'}</td>
            <td className="table-cell text-ink-500 text-xs font-mono break-all">{e.actor || '—'}</td>
            <td
              className={`table-cell text-xs font-mono ${
                (e.status ?? 0) >= 400 ? 'text-danger' : 'text-ink-500'
              }`}
            >
              {e.status || '—'}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function ControlTable({ events }: { events: LogEvent[] }) {
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left border-b border-ink-200 text-ink-500">
          <th className="table-cell font-normal w-44">Time</th>
          <th className="table-cell font-normal">Action</th>
          <th className="table-cell font-normal">Target</th>
          <th className="table-cell font-normal">Actor</th>
        </tr>
      </thead>
      <tbody>
        {events.map((e) => (
          <tr key={e.ts} className="border-b border-ink-100">
            <td className="table-cell text-ink-500 text-xs font-mono">{fmtTime(e.time)}</td>
            <td className="table-cell text-xs">{e.op}</td>
            <td className="table-cell text-ink-500 text-xs font-mono break-all">{e.target || '—'}</td>
            <td className="table-cell text-ink-500 text-xs font-mono break-all">{e.actor || '—'}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export default function LogsPage() {
  const session = loadSession();
  const [category, setCategory] = useState<LogCategory>('data');
  const [events, setEvents] = useState<LogEvent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [exhausted, setExhausted] = useState(false);

  useEffect(() => {
    if (!session) return;
    setEvents(null);
    setExhausted(false);
    setError(null);
    getLogs(session, category, PAGE)
      .then((e) => {
        setEvents(e);
        setExhausted(e.length < PAGE);
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
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
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h2 className="text-base mb-1">Logs</h2>
      <p className="text-xs text-ink-500 mb-4">
        {category === 'data'
          ? 'Object access: who read, wrote, or deleted which object, newest first.'
          : 'Administrative actions: user and configuration changes, newest first.'}
      </p>

      <div className="flex items-center gap-2 mb-6">
        <button type="button" className={tabClass(category === 'data')} onClick={() => setCategory('data')}>
          Access
        </button>
        <button
          type="button"
          className={tabClass(category === 'control')}
          onClick={() => setCategory('control')}
        >
          Control
        </button>
      </div>

      {error && <ErrorBanner message={error} className="mb-4" />}

      {events === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : events.length === 0 ? (
        <p className="text-ink-500 text-sm">
          {category === 'data' ? 'No access events. Enable access logging in Settings.' : 'No control events.'}
        </p>
      ) : (
        <>
          {category === 'data' ? <DataTable events={events} /> : <ControlTable events={events} />}
          {!exhausted && (
            <button className="btn h-8 px-3 text-xs mt-4" disabled={busy} onClick={loadMore}>
              {busy ? 'Loading' : 'Load more'}
            </button>
          )}
        </>
      )}
    </section>
  );
}
