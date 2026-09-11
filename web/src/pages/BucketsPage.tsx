import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { createBucket, deleteBucket, listBuckets, putBucketACL, type CannedACL } from '../lib/s3';
import { getStats } from '../lib/admin';
import { loadSession, type Session } from '../lib/session';
import { formatBytes, formatCount, formatDate, errorMessage } from '../lib/format';
import { ErrorBanner } from '../components/ErrorBanner';
import { Icon } from '../components/icons';
import {
  ConfirmDialog,
  Dialog,
  EmptyState,
  IconButton,
  Loading,
  PageHeader,
  SearchInput,
  Seg,
  Visibility,
} from '../components/ui';

interface Row {
  name: string;
  created?: string;
  acl: CannedACL;
  // Undefined when the stats call failed; the list still renders.
  objects?: number;
  bytes?: number;
}

type Pending = { kind: 'create' } | { kind: 'public'; row: Row } | { kind: 'delete'; row: Row } | null;

// S3 bucket naming as enforced by the server: DNS-label characters only,
// starting and ending alphanumeric, 3 to 63 characters.
const NAME_RE = /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/;

const VISIBILITY_OPTIONS = [
  { key: 'private', label: 'Private' },
  { key: 'public-read', label: 'Public read' },
] as const;

// loadRows joins the S3 listing with per-bucket stats by name. Stats are a
// nice-to-have, so their failure degrades the columns rather than the page.
async function loadRows(session: Session): Promise<Row[]> {
  const [list, stats] = await Promise.all([listBuckets(session), getStats(session).catch(() => null)]);
  const byName = new Map(stats?.perBucket.map((b) => [b.name, b]) ?? []);
  return list.map((b) => {
    const s = byName.get(b.name);
    return { name: b.name, created: b.creationDate, acl: b.acl ?? 'private', objects: s?.objects, bytes: s?.bytes };
  });
}

function summary(rows: Row[]): string {
  const n = rows.length;
  const label = `${formatCount(n)} ${n === 1 ? 'bucket' : 'buckets'}`;
  if (rows.some((r) => r.bytes === undefined)) return label;
  const total = rows.reduce((acc, r) => acc + (r.bytes ?? 0), 0);
  return `${label} · ${formatBytes(total)}`;
}

function NewBucketButton({ onClick }: Readonly<{ onClick: () => void }>) {
  return (
    <button type="button" className="btn-primary" onClick={onClick}>
      <Icon name="plus" />
      New bucket
    </button>
  );
}

function BucketTable({
  rows,
  onToggleACL,
  onDelete,
}: Readonly<{ rows: Row[]; onToggleACL: (row: Row) => void; onDelete: (row: Row) => void }>) {
  const navigate = useNavigate();
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>Name</th>
          <th className="num" style={{ width: 110 }}>
            Objects
          </th>
          <th className="num" style={{ width: 110 }}>
            Size
          </th>
          <th style={{ width: 120 }}>Visibility</th>
          <th style={{ width: 120 }}>Created</th>
          <th style={{ width: 130 }}></th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => {
          const isPublic = r.acl === 'public-read';
          const objectsPath = `/buckets/${encodeURIComponent(r.name)}/objects`;
          return (
            <tr key={r.name}>
              <td className="font-mono text-xs">
                <Link className="hover:underline" to={objectsPath}>
                  {r.name}
                </Link>
              </td>
              <td className="num">{r.objects === undefined ? '—' : formatCount(r.objects)}</td>
              <td className="num text-ink-500">{r.bytes === undefined ? '—' : formatBytes(r.bytes)}</td>
              <td>
                <Visibility acl={r.acl} />
              </td>
              <td className="font-mono text-xs text-ink-500">{formatDate(r.created)}</td>
              <td>
                <div className="acts">
                  <IconButton icon="open" label="Browse objects" onClick={() => navigate(objectsPath)} />
                  <IconButton
                    icon={isPublic ? 'lock' : 'globe'}
                    label={isPublic ? 'Make private' : 'Make public'}
                    onClick={() => onToggleACL(r)}
                  />
                  <IconButton
                    icon="shield"
                    label="Edit CORS"
                    onClick={() => navigate(`/buckets/${encodeURIComponent(r.name)}/cors`)}
                  />
                  <IconButton icon="trash" label="Delete bucket" onClick={() => onDelete(r)} />
                </div>
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function CreateBucketDialog({
  open,
  busy,
  error,
  onCreate,
  onClose,
}: Readonly<{
  open: boolean;
  busy: boolean;
  error: string | null;
  onCreate: (name: string, acl: CannedACL) => void;
  onClose: () => void;
}>) {
  const [name, setName] = useState('');
  const [acl, setAcl] = useState<CannedACL>('private');
  useEffect(() => {
    if (!open) return;
    setName('');
    setAcl('private');
  }, [open]);
  const ready = !busy && NAME_RE.test(name);

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (ready) onCreate(name, acl);
  }

  return (
    <Dialog open={open} title="New bucket" onClose={onClose}>
      <form onSubmit={onSubmit}>
        <p>Lowercase letters, digits and hyphens. 3–63 characters.</p>
        <div className="mt-4 flex flex-col gap-3.5">
          <div>
            <label className="field-label" htmlFor="bucket-name">
              Name
            </label>
            <input
              id="bucket-name"
              className="input-mono"
              value={name}
              autoFocus
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div>
            <span className="field-label">Visibility</span>
            <Seg options={VISIBILITY_OPTIONS} value={acl} onChange={setAcl} label="Visibility" />
            <div className="hint">Private: only signed requests can read objects. Change any time.</div>
          </div>
        </div>
        {error && <ErrorBanner message={error} className="mt-4" />}
        <div className="btns">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn-primary" disabled={!ready}>
            {busy ? 'Working' : 'Create bucket'}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function deleteBody(row: Row): string {
  if (row.objects === undefined || row.objects === 0) return 'This cannot be undone.';
  const size = row.bytes === undefined ? '' : ` (${formatBytes(row.bytes)})`;
  return `The bucket holds ${formatCount(row.objects)} objects${size}. S3 requires a bucket to be empty before it can be deleted. Empty it first.`;
}

export default function BucketsPage() {
  const [session] = useState(loadSession);
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState('');
  const [pending, setPending] = useState<Pending>(null);
  const [busy, setBusy] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!session) return;
    setError(null);
    try {
      setRows(await loadRows(session));
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [session]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  function openDialog(next: Pending) {
    setDialogError(null);
    setPending(next);
  }

  function closeDialog() {
    if (busy) return;
    setPending(null);
  }

  // run performs a dialog-scoped write: failures stay inside the dialog so the
  // operator can correct and retry without losing their place.
  async function run(action: () => Promise<void>) {
    setBusy(true);
    setDialogError(null);
    try {
      await action();
      await refresh();
      setPending(null);
    } catch (e) {
      setDialogError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function onCreate(name: string, acl: CannedACL) {
    if (!session) return;
    await run(async () => {
      await createBucket(session, name);
      if (acl === 'public-read') await putBucketACL(session, name, acl);
    });
  }

  // Widening to public-read goes through a confirm; narrowing back to private
  // is always safe and applies immediately.
  async function onToggleACL(row: Row) {
    if (!session) return;
    if (row.acl !== 'public-read') {
      openDialog({ kind: 'public', row });
      return;
    }
    try {
      await putBucketACL(session, row.name, 'private');
      await refresh();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  const visible = rows?.filter((r) => r.name.includes(filter.trim())) ?? [];
  const target = pending && pending.kind !== 'create' ? pending.row : null;

  return (
    <section>
      <PageHeader
        title="Buckets"
        sub={rows ? summary(rows) : undefined}
        actions={
          <>
            <SearchInput value={filter} onChange={setFilter} placeholder="Filter buckets" label="Filter buckets" />
            <NewBucketButton onClick={() => openDialog({ kind: 'create' })} />
          </>
        }
      />

      {error && <ErrorBanner message={error} className="mb-4" />}

      {rows === null && !error && <Loading />}
      {rows?.length === 0 && (
        <EmptyState text="No buckets yet." action={<NewBucketButton onClick={() => openDialog({ kind: 'create' })} />} />
      )}
      {rows !== null && rows.length > 0 && visible.length === 0 && <EmptyState text="No buckets match the filter." />}
      {visible.length > 0 && (
        <BucketTable rows={visible} onToggleACL={onToggleACL} onDelete={(row) => openDialog({ kind: 'delete', row })} />
      )}

      <CreateBucketDialog
        open={pending?.kind === 'create'}
        busy={busy}
        error={dialogError}
        onCreate={onCreate}
        onClose={closeDialog}
      />
      <ConfirmDialog
        open={pending?.kind === 'public'}
        title={
          <>
            Make <span className="font-mono">{target?.name}</span> public?
          </>
        }
        body="Anyone with the URL can download every object in this bucket, except those with a private object ACL. Listing stays private."
        confirmLabel="Make public"
        busy={busy}
        error={dialogError}
        onConfirm={() => target && session && run(() => putBucketACL(session, target.name, 'public-read'))}
        onClose={closeDialog}
      />
      <ConfirmDialog
        open={pending?.kind === 'delete'}
        title={
          <>
            Delete bucket <span className="font-mono">{target?.name}</span>?
          </>
        }
        body={target ? deleteBody(target) : ''}
        confirmLabel="Delete bucket"
        match={target?.name}
        busy={busy}
        error={dialogError}
        onConfirm={() => target && session && run(() => deleteBucket(session, target.name))}
        onClose={closeDialog}
      />
    </section>
  );
}
