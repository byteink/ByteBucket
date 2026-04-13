import { useEffect, useMemo, useState } from 'react';
import { ACLRule, createUser, CreatedUser, deleteUser, listUsers, updateUserACL, User } from '../lib/admin';
import { loadSession } from '../lib/session';

const defaultACL: ACLRule[] = [{ effect: 'Allow', buckets: ['*'], actions: ['*'] }];

export default function UsersPage() {
  const session = loadSession();
  const [users, setUsers] = useState<User[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<CreatedUser | null>(null);
  const [editing, setEditing] = useState<{ id: string; text: string } | null>(null);

  async function refresh() {
    if (!session) return;
    setError(null);
    try {
      setUsers(await listUsers(session));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const sorted = useMemo(
    () => (users ?? []).slice().sort((a, b) => a.accessKeyID.localeCompare(b.accessKeyID)),
    [users]
  );

  async function onCreate() {
    if (!session) return;
    try {
      const user = await createUser(session, defaultACL);
      setCreated(user);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onDelete(id: string) {
    if (!session) return;
    if (!window.confirm(`Delete user ${id}?`)) return;
    try {
      await deleteUser(session, id);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function onSaveACL() {
    if (!session || !editing) return;
    let parsed: ACLRule[];
    try {
      parsed = JSON.parse(editing.text) as ACLRule[];
    } catch {
      setError('ACL must be valid JSON (array of rules)');
      return;
    }
    try {
      await updateUserACL(session, editing.id, parsed);
      setEditing(null);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <section>
      <div className="flex items-baseline justify-between mb-6">
        <h2 className="text-base">Users</h2>
        <button className="btn-primary" onClick={onCreate}>New user</button>
      </div>

      {error && <div className="text-xs text-ink-900 border-l-2 border-ink-900 pl-3 mb-4">{error}</div>}

      {users === null ? (
        <p className="text-ink-500 text-sm">Loading.</p>
      ) : sorted.length === 0 ? (
        <p className="text-ink-500 text-sm">No users.</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left border-b border-ink-200 text-ink-500">
              <th className="table-cell font-normal">Access Key ID</th>
              <th className="table-cell font-normal">Rules</th>
              <th className="table-cell font-normal w-40"></th>
            </tr>
          </thead>
          <tbody>
            {sorted.map((u) => (
              <tr key={u.accessKeyID} className="border-b border-ink-100">
                <td className="table-cell font-mono text-xs">{u.accessKeyID}</td>
                <td className="table-cell text-ink-500 text-xs">{(u.acl ?? []).length} rule(s)</td>
                <td className="table-cell text-right">
                  <button
                    className="btn h-7 px-2 text-xs mr-2"
                    onClick={() =>
                      setEditing({ id: u.accessKeyID, text: JSON.stringify(u.acl ?? [], null, 2) })
                    }
                  >
                    Edit ACL
                  </button>
                  <button className="btn-danger h-7 px-2 text-xs" onClick={() => onDelete(u.accessKeyID)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {created && <CreatedUserModal user={created} onClose={() => setCreated(null)} />}

      {editing && (
        <div className="fixed inset-0 bg-ink-900/40 flex items-center justify-center p-6 z-10">
          <div className="bg-ink-0 border border-ink-200 w-full max-w-xl p-6">
            <h3 className="text-sm font-mono mb-4">Edit ACL — {editing.id}</h3>
            <textarea
              className="w-full h-64 border border-ink-200 p-2 font-mono text-xs focus:outline-none focus:border-ink-900"
              value={editing.text}
              onChange={(e) => setEditing({ ...editing, text: e.target.value })}
            />
            <div className="flex justify-end gap-2 mt-4">
              <button className="btn" onClick={() => setEditing(null)}>Cancel</button>
              <button className="btn-primary" onClick={onSaveACL}>Save</button>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}

function CreatedUserModal({ user, onClose }: { user: CreatedUser; onClose: () => void }) {
  async function copy(value: string) {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      /* ignore */
    }
  }
  return (
    <div className="fixed inset-0 bg-ink-900/40 flex items-center justify-center p-6 z-10">
      <div className="bg-ink-0 border border-ink-200 w-full max-w-md p-6">
        <h3 className="text-sm font-mono mb-2">User created</h3>
        <p className="text-xs text-ink-500 mb-4">
          Copy the secret now. It will not be shown again.
        </p>
        <div className="space-y-3">
          <KeyRow label="Access Key ID" value={user.accessKeyID} onCopy={() => copy(user.accessKeyID)} />
          <KeyRow label="Secret" value={user.secretAccessKey} onCopy={() => copy(user.secretAccessKey)} />
        </div>
        <div className="flex justify-end mt-6">
          <button className="btn-primary" onClick={onClose}>Done</button>
        </div>
      </div>
    </div>
  );
}

function KeyRow({ label, value, onCopy }: { label: string; value: string; onCopy: () => void }) {
  return (
    <div>
      <div className="field-label">{label}</div>
      <div className="flex gap-2">
        <code className="flex-1 border border-ink-200 px-2 h-9 flex items-center overflow-x-auto text-xs">
          {value}
        </code>
        <button className="btn text-xs" onClick={onCopy}>Copy</button>
      </div>
    </div>
  );
}
