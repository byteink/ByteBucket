import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  createUser,
  deleteUser,
  listUsers,
  updateUserACL,
  type ACLRule,
  type CreatedUser,
  type User,
} from '../lib/admin';
import { loadSession } from '../lib/session';
import { errorMessage, formatDate } from '../lib/format';
import { ErrorBanner } from '../components/ErrorBanner';
import {
  Badge,
  ConfirmDialog,
  CopyButton,
  Dialog,
  Drawer,
  EmptyState,
  IconButton,
  Loading,
  PageHeader,
  SearchInput,
  Tip,
} from '../components/ui';
import { Icon } from '../components/icons';
import { AccessEditor, isAdminACL, shortAction } from '../components/AccessEditor';

const ACTION_WORDS: Record<string, string> = {
  's3:GetObject': 'read',
  's3:PutObject': 'write',
  's3:DeleteObject': 'delete',
  's3:ListBucket': 'list',
};

function describeRule(r: ACLRule): string {
  const actions = r.actions.map((a) => ACTION_WORDS[a] ?? shortAction(a)).join(', ');
  const deny = r.effect.toLowerCase() === 'allow' ? '' : 'deny ';
  return `${deny}${r.buckets.join(', ')} · ${actions}`;
}

// The bucket input is free text, so trailing commas and stray spaces are
// dropped here rather than while the operator is still typing.
function normalizeRules(rules: ACLRule[]): ACLRule[] {
  return rules.map((r) => ({
    effect: r.effect,
    buckets: r.buckets.map((b) => b.trim()).filter(Boolean),
    actions: r.actions.map((a) => a.trim()).filter(Boolean),
  }));
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`;
}

interface Editing {
  id: string;
  rules: ACLRule[];
}

export default function UsersPage() {
  const [session] = useState(loadSession);
  const [users, setUsers] = useState<User[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState('');
  const [created, setCreated] = useState<CreatedUser | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [draft, setDraft] = useState<ACLRule[] | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    if (!session) return;
    setError(null);
    try {
      setUsers(await listUsers(session));
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [session]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const sorted = useMemo(
    () => (users ?? []).slice().sort((a, b) => a.accessKeyID.localeCompare(b.accessKeyID)),
    [users],
  );
  const needle = filter.trim().toLowerCase();
  const visible = needle ? sorted.filter((u) => u.accessKeyID.toLowerCase().includes(needle)) : sorted;
  const admins = sorted.filter((u) => isAdminACL(u.acl ?? [])).length;

  async function onCreate() {
    if (!session) return;
    try {
      setCreated(await createUser(session, []));
      await refresh();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function onDelete() {
    if (!session || !deleting) return;
    setBusy(true);
    setDialogError(null);
    try {
      await deleteUser(session, deleting);
      setDeleting(null);
      await refresh();
    } catch (e) {
      setDialogError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  function openEditor(id: string, rules: ACLRule[]) {
    setDialogError(null);
    setDraft(rules);
    setEditing({ id, rules });
  }

  function closeEditor() {
    setEditing(null);
    setDraft(null);
    setDialogError(null);
  }

  async function onSaveAccess() {
    if (!session || !editing || draft === null) return;
    setBusy(true);
    setDialogError(null);
    try {
      await updateUserACL(session, editing.id, normalizeRules(draft));
      closeEditor();
      await refresh();
    } catch (e) {
      setDialogError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <PageHeader
        title="Users"
        sub={users ? `${plural(users.length, 'access key')} · ${plural(admins, 'admin')}` : undefined}
        actions={
          <>
            <SearchInput
              value={filter}
              onChange={setFilter}
              placeholder="Filter by access key"
              label="Filter by access key"
            />
            <button type="button" className="btn-primary" onClick={onCreate}>
              <Icon name="plus" />
              New user
            </button>
          </>
        }
      />

      {error && <ErrorBanner message={error} className="mb-4" />}

      {users === null ? (
        <Loading />
      ) : visible.length === 0 ? (
        <EmptyState text={needle ? 'No users match this filter.' : 'No users.'} />
      ) : (
        <UsersTable
          users={visible}
          self={session?.accessKey ?? ''}
          onEdit={(u) => openEditor(u.accessKeyID, u.acl ?? [])}
          onDelete={setDeleting}
        />
      )}

      <ConfirmDialog
        open={deleting !== null}
        title={`Delete user ${deleting ?? ''}?`}
        body="Requests signed with this key stop working immediately."
        confirmLabel="Delete user"
        busy={busy}
        error={dialogError}
        onConfirm={onDelete}
        onClose={() => {
          setDeleting(null);
          setDialogError(null);
        }}
      />

      <CreatedDialog
        user={created}
        onDone={() => setCreated(null)}
        onGrant={() => {
          if (!created) return;
          const id = created.accessKeyID;
          setCreated(null);
          openEditor(id, []);
        }}
      />

      <Drawer
        open={editing !== null}
        title={
          <>
            Edit access <span className="font-mono font-normal text-ink-500 ml-1.5">{editing?.id}</span>
          </>
        }
        onClose={closeEditor}
        footer={
          <>
            <button type="button" className="btn" onClick={closeEditor} disabled={busy}>
              Cancel
            </button>
            <button type="button" className="btn-primary" onClick={onSaveAccess} disabled={busy || draft === null}>
              {busy ? 'Working' : 'Save access'}
            </button>
          </>
        }
      >
        {editing && <AccessEditor key={editing.id} initial={editing.rules} error={dialogError} onChange={setDraft} />}
      </Drawer>
    </section>
  );
}

function UsersTable({
  users,
  self,
  onEdit,
  onDelete,
}: Readonly<{ users: User[]; self: string; onEdit: (u: User) => void; onDelete: (id: string) => void }>) {
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>Access key ID</th>
          <th className="w-[100px]">Role</th>
          <th>Access</th>
          <th className="w-[120px]">Created</th>
          <th className="w-20"></th>
        </tr>
      </thead>
      <tbody>
        {users.map((u) => (
          <UserRow key={u.accessKeyID} user={u} isSelf={u.accessKeyID === self} onEdit={onEdit} onDelete={onDelete} />
        ))}
      </tbody>
    </table>
  );
}

function UserRow({
  user,
  isSelf,
  onEdit,
  onDelete,
}: Readonly<{ user: User; isSelf: boolean; onEdit: (u: User) => void; onDelete: (id: string) => void }>) {
  const rules = user.acl ?? [];
  const admin = isAdminACL(rules);
  return (
    <tr>
      <td className="font-mono">
        {user.accessKeyID}
        {isSelf && <span className="font-sans text-xs text-ink-500"> (you)</span>}
      </td>
      <td>{admin ? <Badge>Admin</Badge> : <span className="text-xs text-ink-500">User</span>}</td>
      <td>
        <AccessSummary rules={rules} admin={admin} />
      </td>
      <td className="font-mono text-ink-500">{formatDate(user.createdAt)}</td>
      <td>
        <div className="acts">
          <IconButton icon="pencil" label="Edit access" onClick={() => onEdit(user)} />
          <IconButton
            icon="trash"
            label={isSelf ? 'You cannot delete your own key' : 'Delete user'}
            disabled={isSelf}
            onClick={() => onDelete(user.accessKeyID)}
          />
        </div>
      </td>
    </tr>
  );
}

function AccessSummary({ rules, admin }: Readonly<{ rules: ACLRule[]; admin: boolean }>) {
  if (admin) return <>All buckets, all actions</>;
  if (rules.length === 0) {
    return (
      <span className="text-ink-500 inline-flex items-center gap-1.5">
        No rules
        <Tip pos="below" text={'This key cannot do anything yet.\nGrant access to use it.'}>
          <Icon name="info" size={14} />
        </Tip>
      </span>
    );
  }
  return <>{rules.map(describeRule).join('; ')}</>;
}

// The secret is shown exactly once: the server stores it encrypted and has
// no endpoint to read it back, so the dialog is the only chance to copy it.
function CreatedDialog({
  user,
  onDone,
  onGrant,
}: Readonly<{ user: CreatedUser | null; onDone: () => void; onGrant: () => void }>) {
  return (
    <Dialog open={user !== null} title="User created" onClose={onDone}>
      <p>
        Copy the secret now. It is shown once and cannot be recovered. The key has no access until you grant rules.
      </p>
      <div className="mt-4 flex flex-col gap-3">
        <SecretField id="created-ak" label="Access key ID" value={user?.accessKeyID ?? ''} />
        <SecretField id="created-sk" label="Secret access key" value={user?.secretAccessKey ?? ''} />
      </div>
      <div className="btns">
        <button type="button" className="btn" onClick={onDone}>
          Done
        </button>
        <button type="button" className="btn-primary" onClick={onGrant}>
          Grant access
        </button>
      </div>
    </Dialog>
  );
}

function SecretField({ id, label, value }: Readonly<{ id: string; label: string; value: string }>) {
  return (
    <div>
      <label className="field-label" htmlFor={id}>
        {label}
      </label>
      <div className="flex gap-1.5">
        <input id={id} className="input-mono" readOnly value={value} />
        <CopyButton value={value} />
      </div>
    </div>
  );
}
