import { useEffect, useState } from 'react';
import { getAudit, type AuditEvent } from '../lib/admin';
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

export default function AuditPage() {
  const session = loadSession();
  const [events, setEvents] = useState<AuditEvent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [exhausted, setExhausted] = useState(false);

  useEffect(() => {
    if (!session) return;
    getAudit(session, PAGE)
      .then((e) => {
        setEvents(e);
        setExhausted(e.length < PAGE);
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
    // session is read once from localStorage; depending on its identity would loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function loadMore() {
    if (!session || !events || events.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const older = await getAudit(session, PAGE, events[events.length - 1].ts);
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
      <h2 className="text-base mb-1">Audit log</h2>
      <p className="text-xs text-ink-500 mb-6">
        Administrative actions: user and configuration changes, newest first.
      </p>

      {error && <ErrorBanner message={error} className="mb-4" />}

      {events === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : events.length === 0 ? (
        <p className="text-ink-500 text-sm">No audit events.</p>
      ) : (
        <>
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
                  <td className="table-cell text-xs">{e.action}</td>
                  <td className="table-cell text-ink-500 text-xs font-mono break-all">{e.target || '—'}</td>
                  <td className="table-cell text-ink-500 text-xs font-mono break-all">{e.actor || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
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
