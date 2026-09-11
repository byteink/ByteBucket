import { useEffect, useMemo, useRef, useState, type ChangeEvent, type ReactNode } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import {
  copyObject,
  deleteObject,
  deleteObjects,
  downloadObject,
  getConfig,
  publicObjectURL,
  putObject,
  putObjectACL,
  type CannedACL,
} from '../lib/s3';
import { buildCrumbs, objectDetailPath } from '../lib/paths';
import { loadSession } from '../lib/session';
import { copyText } from '../lib/clipboard';
import { errorMessage, formatBytes, formatCount, formatDateTime } from '../lib/format';
import { useObjectListing, type ObjectRow } from '../lib/useObjectListing';
import { ErrorBanner } from '../components/ErrorBanner';
import { Icon } from '../components/icons';
import {
  Badge,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Loading,
  PageHeader,
  PromptDialog,
  SearchInput,
  Tip,
  Visibility,
} from '../components/ui';

type DialogState =
  | { kind: 'delete'; keys: string[] }
  | { kind: 'publish'; keys: string[] }
  | { kind: 'move'; key: string }
  | null;

interface RowActions {
  download: (key: string) => void;
  copyURL: (key: string) => void;
  move: (key: string) => void;
  toggleACL: (row: ObjectRow) => void;
  remove: (key: string) => void;
}

function plural(n: number, noun: string): string {
  return `${formatCount(n)} ${noun}${n === 1 ? '' : 's'}`;
}

export default function ObjectsPage() {
  const { name } = useParams<{ name: string }>();
  const bucket = name ?? '';
  const [params, setParams] = useSearchParams();
  // Current folder, always either "" (bucket root) or ends with "/".
  const prefix = params.get('prefix') ?? '';
  const session = useMemo(() => loadSession(), []);
  const listing = useObjectListing(session, bucket, prefix);
  const { rows, folders, bucketACL, error, hasMore, refresh, setError } = listing;
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [filter, setFilter] = useState('');
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const [publicBase, setPublicBase] = useState('');
  const [dialog, setDialog] = useState<DialogState>(null);
  const [dialogBusy, setDialogBusy] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!session) return;
    getConfig(session)
      .then((cfg) => setPublicBase(cfg.publicBaseURL))
      .catch((e: unknown) => setError(errorMessage(e)));
  }, [session, setError]);

  useEffect(() => {
    if (!copiedKey) return;
    const t = setTimeout(() => setCopiedKey(null), 1500);
    return () => clearTimeout(t);
  }, [copiedKey]);

  // Selection is keyed on the listing; anything that reloads it drops the
  // selection so a stale key can never be acted on.
  async function reload() {
    await refresh();
    setSelected(new Set());
  }

  async function run(op: () => Promise<void>) {
    try {
      await op();
      await reload();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function uploadFiles(files: FileList | File[]) {
    if (!session || !bucket) return;
    await run(async () => {
      for (const file of Array.from(files)) {
        await putObject(session, bucket, prefix + file.name, file);
      }
    });
  }
  const dragging = usePageDrop(uploadFiles);

  function navigate(nextPrefix: string) {
    const next = new URLSearchParams(params);
    if (nextPrefix) next.set('prefix', nextPrefix);
    else next.delete('prefix');
    setParams(next);
    setFilter('');
  }

  function toggleSelected(key: string) {
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function openDialog(next: DialogState) {
    setDialogError(null);
    setDialog(next);
  }

  // Dialog work reports failures inside the dialog and only closes on success.
  async function runDialog(op: () => Promise<void>) {
    setDialogBusy(true);
    setDialogError(null);
    try {
      await op();
      setDialog(null);
      await reload();
    } catch (e) {
      setDialogError(errorMessage(e));
    } finally {
      setDialogBusy(false);
    }
  }

  async function setACL(keys: string[], acl: CannedACL) {
    if (!session) return;
    for (const key of keys) await putObjectACL(session, bucket, key, acl);
  }

  async function removeKeys(keys: string[]) {
    if (!session) return;
    const failures = await deleteObjects(session, bucket, keys);
    if (failures.length > 0) {
      throw new Error(`${failures.length} object(s) could not be deleted: ${failures.map((f) => f.key).join(', ')}`);
    }
  }

  async function moveKey(src: string, dst: string) {
    if (!session) return;
    // Copy then delete the source: the closest S3 has to an atomic rename.
    await copyObject(session, bucket, src, dst);
    await deleteObject(session, bucket, src);
  }

  const actions: RowActions = {
    download: (key) => {
      if (session) downloadObject(session, bucket, key).catch((e: unknown) => setError(errorMessage(e)));
    },
    copyURL: (key) => {
      copyText(publicObjectURL(publicBase, bucket, key))
        .then(() => setCopiedKey(key))
        .catch((e: unknown) => setError(errorMessage(e)));
    },
    move: (key) => openDialog({ kind: 'move', key }),
    toggleACL: (row) => {
      if (row.acl === 'public-read') void run(() => setACL([row.key], 'private'));
      else openDialog({ kind: 'publish', keys: [row.key] });
    },
    remove: (key) => openDialog({ kind: 'delete', keys: [key] }),
  };

  function onInput(e: ChangeEvent<HTMLInputElement>) {
    if (e.target.files && e.target.files.length > 0) {
      void uploadFiles(e.target.files);
      e.target.value = '';
    }
  }

  const q = filter.trim().toLowerCase();
  const shownFolders = folders.filter((p) => !q || p.slice(prefix.length).toLowerCase().includes(q));
  const shownRows = (rows ?? []).filter((o) => !q || o.key.slice(prefix.length).toLowerCase().includes(q));
  const totalBytes = (rows ?? []).reduce((acc, o) => acc + o.size, 0);
  const folderName = prefix ? prefix.slice(0, -1).split('/').pop() ?? bucket : bucket;
  const selectedKeys = Array.from(selected);

  const uploadButton = (
    <button type="button" className="btn-primary" onClick={() => fileInput.current?.click()}>
      <Icon name="upload" />
      Upload
    </button>
  );

  return (
    <section>
      <Crumbs bucket={bucket} prefix={prefix} onNavigate={navigate} />
      <PageHeader
        title={folderName}
        badge={
          bucketACL === 'public-read' ? (
            <Badge tip={'Every object is anonymously readable\nunless its own ACL says private.'}>Public bucket</Badge>
          ) : undefined
        }
        sub={
          rows === null
            ? undefined
            : `${plural(folders.length, 'folder')} · ${plural(rows.length, 'object')} · ${formatBytes(totalBytes)} in this folder${hasMore ? ' · more not loaded' : ''}`
        }
        actions={
          <>
            <SearchInput value={filter} onChange={setFilter} placeholder="Filter this folder" label="Filter this folder" />
            {uploadButton}
          </>
        }
      />
      <input ref={fileInput} type="file" multiple className="hidden" tabIndex={-1} onChange={onInput} />

      {error && <ErrorBanner message={error} className="mb-4" />}

      {rows === null ? (
        <Loading />
      ) : rows.length === 0 && folders.length === 0 ? (
        <EmptyState text={prefix ? 'Empty folder.' : 'Empty bucket.'} action={uploadButton} />
      ) : (
        <>
          {selected.size > 0 && (
            <SelectionBar
              count={selected.size}
              onPrivate={() => void run(() => setACL(selectedKeys, 'private'))}
              onPublic={() => openDialog({ kind: 'publish', keys: selectedKeys })}
              onDelete={() => openDialog({ kind: 'delete', keys: selectedKeys })}
              onClear={() => setSelected(new Set())}
            />
          )}
          <ObjectTable
            bucket={bucket}
            prefix={prefix}
            bucketACL={bucketACL}
            folders={shownFolders}
            rows={shownRows}
            selected={selected}
            copiedKey={copiedKey}
            onToggle={toggleSelected}
            onOpenFolder={navigate}
            actions={actions}
          />
          <div className="flex items-center gap-4 mt-3 text-xs text-ink-500">
            <span>
              Showing {plural(shownFolders.length, 'folder')} and {plural(shownRows.length, 'object')}
            </span>
            {hasMore && (
              <button type="button" className="btn-sm" onClick={() => void listing.loadMore()} disabled={listing.loadingMore}>
                {listing.loadingMore ? 'Loading' : 'Load more'}
              </button>
            )}
            <span className="ml-auto">
              Drop files anywhere to upload into <span className="font-mono">{prefix || `${bucket}/`}</span>
            </span>
          </div>
        </>
      )}

      {dragging && (
        <div className="fixed inset-0 z-40 bg-ink-0/90 border-2 border-dashed border-ink-900 flex items-center justify-center text-sm pointer-events-none">
          Drop to upload into <span className="font-mono ml-1.5">{prefix || bucket}</span>
        </div>
      )}

      <ObjectDialogs
        dialog={dialog}
        busy={dialogBusy}
        error={dialogError}
        onClose={() => setDialog(null)}
        onDelete={(keys) => void runDialog(() => removeKeys(keys))}
        onPublish={(keys) => void runDialog(() => setACL(keys, 'public-read'))}
        onMove={(src, dst) => void runDialog(() => moveKey(src, dst))}
      />
    </section>
  );
}

// usePageDrop turns the whole page into a drop target. A depth counter is
// needed because dragenter/dragleave fire for every child element crossed.
function usePageDrop(onFiles: (files: FileList) => Promise<void>): boolean {
  const [dragging, setDragging] = useState(false);
  const handler = useRef(onFiles);
  handler.current = onFiles;

  useEffect(() => {
    let depth = 0;
    const hasFiles = (e: DragEvent) => e.dataTransfer?.types.includes('Files') ?? false;
    const onEnter = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      depth++;
      setDragging(true);
    };
    const onLeave = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      depth = Math.max(0, depth - 1);
      if (depth === 0) setDragging(false);
    };
    const onOver = (e: DragEvent) => {
      if (hasFiles(e)) e.preventDefault();
    };
    const onDrop = (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth = 0;
      setDragging(false);
      const files = e.dataTransfer?.files;
      if (files && files.length > 0) void handler.current(files);
    };
    document.addEventListener('dragenter', onEnter);
    document.addEventListener('dragleave', onLeave);
    document.addEventListener('dragover', onOver);
    document.addEventListener('drop', onDrop);
    return () => {
      document.removeEventListener('dragenter', onEnter);
      document.removeEventListener('dragleave', onLeave);
      document.removeEventListener('dragover', onOver);
      document.removeEventListener('drop', onDrop);
    };
  }, []);

  return dragging;
}

function Crumbs({
  bucket,
  prefix,
  onNavigate,
}: Readonly<{ bucket: string; prefix: string; onNavigate: (prefix: string) => void }>) {
  const crumbs = buildCrumbs(prefix);
  const last = crumbs.length - 1;
  return (
    <nav className="crumb" aria-label="Breadcrumb">
      <Link to="/buckets">Buckets</Link>
      <span>/</span>
      {crumbs.length === 0 ? (
        <span className="here">{bucket}</span>
      ) : (
        <button type="button" className="font-mono" onClick={() => onNavigate('')}>
          {bucket}
        </button>
      )}
      {crumbs.map((c, i) => (
        <span key={c.prefix} className="contents">
          <span>/</span>
          {i === last ? (
            <span className="here">{c.label}</span>
          ) : (
            <button type="button" className="font-mono" onClick={() => onNavigate(c.prefix)}>
              {c.label}
            </button>
          )}
        </span>
      ))}
    </nav>
  );
}

function SelectionBar({
  count,
  onPrivate,
  onPublic,
  onDelete,
  onClear,
}: Readonly<{ count: number; onPrivate: () => void; onPublic: () => void; onDelete: () => void; onClear: () => void }>) {
  return (
    <div className="sel-bar" role="toolbar" aria-label="Selection">
      <span className="font-medium mr-2">{formatCount(count)} selected</span>
      <button type="button" className="btn-sm" onClick={onPrivate}>
        <Icon name="lock" />
        Make private
      </button>
      <button type="button" className="btn-sm" onClick={onPublic}>
        <Icon name="globe" />
        Make public
      </button>
      <button type="button" className="btn-danger btn-sm" onClick={onDelete}>
        <Icon name="trash" />
        Delete
      </button>
      <button type="button" className="btn-ghost btn-sm ml-auto" onClick={onClear}>
        Clear
      </button>
    </div>
  );
}

function ObjectTable({
  bucket,
  prefix,
  bucketACL,
  folders,
  rows,
  selected,
  copiedKey,
  onToggle,
  onOpenFolder,
  actions,
}: Readonly<{
  bucket: string;
  prefix: string;
  bucketACL: CannedACL;
  folders: string[];
  rows: ObjectRow[];
  selected: Set<string>;
  copiedKey: string | null;
  onToggle: (key: string) => void;
  onOpenFolder: (prefix: string) => void;
  actions: RowActions;
}>) {
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th className="w-6"></th>
          <th>Name</th>
          <th className="text-right">Size</th>
          <th className="w-[140px]">Modified</th>
          <th className="w-[150px]">Visibility</th>
          <th className="w-40"></th>
        </tr>
      </thead>
      <tbody>
        {folders.map((p) => (
          <tr key={p}>
            <td></td>
            <td className="font-mono">
              <span className="flex items-center gap-2">
                <span className="text-ink-500 shrink-0 flex">
                  <Icon name="folder" />
                </span>
                <button type="button" className="hover:underline text-left break-all" onClick={() => onOpenFolder(p)}>
                  {p.slice(prefix.length)}
                </button>
              </span>
            </td>
            <td className="num text-ink-500">—</td>
            <td className="text-ink-500">—</td>
            <td></td>
            <td></td>
          </tr>
        ))}
        {rows.map((o) => (
          <tr key={o.key}>
            <td>
              <input
                type="checkbox"
                className="block w-3.5 h-3.5 m-0 accent-ink-900"
                aria-label={`Select ${o.key}`}
                checked={selected.has(o.key)}
                onChange={() => onToggle(o.key)}
              />
            </td>
            <td className="font-mono break-all">
              <Link className="hover:underline" to={objectDetailPath(bucket, o.key)}>
                {o.key.slice(prefix.length)}
              </Link>
            </td>
            <td className="num text-ink-500">{formatBytes(o.size)}</td>
            <td className="text-ink-500">{o.modified ? formatDateTime(o.modified) : '—'}</td>
            <td>
              <VisibilityCell row={o} bucketACL={bucketACL} />
            </td>
            <td>
              <div className="acts">
                <IconButton icon="download" label="Download" onClick={() => actions.download(o.key)} />
                <IconButton
                  icon="link"
                  label={copiedKey === o.key ? 'Copied' : 'Copy public URL'}
                  className={copiedKey === o.key ? 'tt-on' : ''}
                  onClick={() => actions.copyURL(o.key)}
                />
                <IconButton icon="pencil" label="Rename or move" onClick={() => actions.move(o.key)} />
                <IconButton
                  icon={o.acl === 'public-read' ? 'lock' : 'globe'}
                  label={o.acl === 'public-read' ? 'Make private' : 'Make public'}
                  onClick={() => actions.toggleACL(o)}
                />
                <IconButton icon="trash" label="Delete" onClick={() => actions.remove(o.key)} />
              </div>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function VisibilityCell({ row, bucketACL }: Readonly<{ row: ObjectRow; bucketACL: CannedACL }>) {
  const override = bucketACL === 'public-read' && row.acl === 'private';
  return (
    <span className="inline-flex items-center gap-1.5">
      <Visibility acl={row.acl} source={row.aclSource} />
      {override && (
        <Tip text="Object ACL overrides the public bucket." pos="below">
          <span className="text-xs text-ink-500 border-b border-dotted border-ink-300 cursor-default">override</span>
        </Tip>
      )}
    </span>
  );
}

function keyList(keys: string[]): ReactNode {
  const shown = keys.slice(0, 5);
  return (
    <>
      <ul className="font-mono mt-2 flex flex-col gap-0.5 break-all">
        {shown.map((k) => (
          <li key={k}>{k}</li>
        ))}
      </ul>
      {keys.length > shown.length && <span className="block mt-1">and {formatCount(keys.length - shown.length)} more.</span>}
    </>
  );
}

function ObjectDialogs({
  dialog,
  busy,
  error,
  onClose,
  onDelete,
  onPublish,
  onMove,
}: Readonly<{
  dialog: DialogState;
  busy: boolean;
  error: string | null;
  onClose: () => void;
  onDelete: (keys: string[]) => void;
  onPublish: (keys: string[]) => void;
  onMove: (src: string, dst: string) => void;
}>) {
  const del = dialog?.kind === 'delete' ? dialog.keys : null;
  const pub = dialog?.kind === 'publish' ? dialog.keys : null;
  const mv = dialog?.kind === 'move' ? dialog.key : null;
  const one = (keys: string[]) => keys.length === 1;
  return (
    <>
      <ConfirmDialog
        open={del !== null}
        title={del && one(del) ? `Delete ${del[0]}?` : `Delete ${formatCount(del?.length ?? 0)} objects?`}
        body={del && one(del) ? 'This cannot be undone.' : <>This cannot be undone.{del && keyList(del)}</>}
        confirmLabel={del && one(del) ? 'Delete object' : `Delete ${formatCount(del?.length ?? 0)} objects`}
        busy={busy}
        error={error}
        onConfirm={() => del && onDelete(del)}
        onClose={onClose}
      />
      <ConfirmDialog
        open={pub !== null}
        title={pub && one(pub) ? `Make ${pub[0]} public?` : `Make ${formatCount(pub?.length ?? 0)} objects public?`}
        body={
          pub && one(pub)
            ? 'Anyone with the URL will be able to download this object.'
            : 'Anyone with the URL will be able to download these objects.'
        }
        confirmLabel="Make public"
        busy={busy}
        error={error}
        onConfirm={() => pub && onPublish(pub)}
        onClose={onClose}
      />
      <PromptDialog
        open={mv !== null}
        title="Rename or move"
        body="The object is copied to the new key, then the old key is deleted."
        label="New key"
        initial={mv ?? ''}
        submitLabel="Move"
        busy={busy}
        error={error}
        onSubmit={(dst) => mv && onMove(mv, dst)}
        onClose={onClose}
      />
    </>
  );
}
