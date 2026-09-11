// AccessEditor is the body of the "Edit access" drawer: a structured rule
// editor and a JSON textarea bound to the same rules. It owns the last valid
// rule set so a half-typed JSON edit never wipes the structured view.
import { useState } from 'react';
import type { ACLRule } from '../lib/admin';
import { IconButton, Seg } from './ui';
import { Icon } from './icons';
import { ErrorBanner } from './ErrorBanner';

// adminACL is the exact shape the server's admin check matches on; the Role
// switch writes it verbatim so operators never hand-craft the wildcard rule.
export const adminACL: ACLRule[] = [{ effect: 'Allow', buckets: ['*'], actions: ['*'] }];

export function isAdminRule(r: ACLRule): boolean {
  return r.effect.toLowerCase() === 'allow' && r.buckets.includes('*') && r.actions.includes('*');
}

export function isAdminACL(rules: ACLRule[]): boolean {
  return rules.some(isAdminRule);
}

// The four actions every scoped key is expected to mix and match. Anything
// else the server knows (CreateBucket, ListBuckets, ...) is preserved and
// shown as an extra checkbox rather than silently dropped on save.
const STANDARD_ACTIONS = ['s3:GetObject', 's3:PutObject', 's3:DeleteObject', 's3:ListBucket'] as const;

export function shortAction(action: string): string {
  return action.startsWith('s3:') ? action.slice(3) : action;
}

function isStringArray(v: unknown): v is string[] {
  return Array.isArray(v) && v.every((s) => typeof s === 'string');
}

function isRule(v: unknown): v is ACLRule {
  if (typeof v !== 'object' || v === null) return false;
  const r = v as Record<string, unknown>;
  return typeof r.effect === 'string' && isStringArray(r.buckets) && isStringArray(r.actions);
}

// parseRules accepts only the wire shape the server binds; a looser parse
// would let a typo save a rule the server rejects with a bare 400.
export function parseRules(text: string): { rules: ACLRule[] } | { error: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (e) {
    return { error: e instanceof Error ? e.message : 'Invalid JSON' };
  }
  if (!Array.isArray(parsed)) return { error: 'ACL must be an array of rules' };
  const bad = parsed.findIndex((r) => !isRule(r));
  if (bad !== -1) return { error: `Rule ${bad + 1} needs effect, buckets[] and actions[]` };
  return { rules: parsed.map((r: ACLRule) => ({ effect: r.effect, buckets: r.buckets, actions: r.actions })) };
}

function toJSON(rules: ACLRule[]): string {
  return JSON.stringify(rules, null, 2);
}

type Role = 'admin' | 'scoped';

export function AccessEditor({
  initial,
  error,
  onChange,
}: Readonly<{ initial: ACLRule[]; error?: string | null; onChange: (rules: ACLRule[] | null) => void }>) {
  const [rules, setRules] = useState<ACLRule[]>(initial);
  const [json, setJson] = useState(() => toJSON(initial));
  const [jsonError, setJsonError] = useState<string | null>(null);

  function commit(next: ACLRule[]) {
    setRules(next);
    setJson(toJSON(next));
    setJsonError(null);
    onChange(next);
  }

  function onJson(text: string) {
    setJson(text);
    const result = parseRules(text);
    if ('error' in result) {
      setJsonError(result.error);
      onChange(null);
      return;
    }
    setRules(result.rules);
    setJsonError(null);
    onChange(result.rules);
  }

  function onRole(role: Role) {
    commit(role === 'admin' ? adminACL : rules.filter((r) => !isAdminRule(r)));
  }

  function updateRule(i: number, patch: Partial<ACLRule>) {
    commit(rules.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }

  const role: Role = isAdminACL(rules) ? 'admin' : 'scoped';

  return (
    <>
      {error && <ErrorBanner message={error} />}
      <div>
        <span className="field-label">Role</span>
        <Seg<Role>
          label="Role"
          value={role}
          onChange={onRole}
          options={[
            { key: 'admin', label: 'Admin' },
            { key: 'scoped', label: 'Scoped' },
          ]}
        />
        <div className="hint">Admin is the single rule Allow · * · *. It also unlocks this dashboard.</div>
      </div>
      {role === 'scoped' && (
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <span className="field-label mb-0">Rules · {rules.length}</span>
            <button
              type="button"
              className="btn-ghost btn-sm"
              onClick={() => commit([...rules, { effect: 'Allow', buckets: [''], actions: [] }])}
            >
              <Icon name="plus" />
              Add rule
            </button>
          </div>
          {rules.map((r, i) => (
            <RuleBlock
              key={i}
              index={i}
              rule={r}
              onChange={(patch) => updateRule(i, patch)}
              onRemove={() => commit(rules.filter((_, j) => j !== i))}
            />
          ))}
        </div>
      )}
      <details>
        <summary className="text-xs text-ink-500 cursor-pointer">Edit as JSON</summary>
        <label className="sr-only" htmlFor="acl-json">
          ACL JSON
        </label>
        <textarea
          id="acl-json"
          className="input-mono mt-2 h-[140px] p-2 resize-y"
          spellCheck={false}
          value={json}
          aria-invalid={jsonError !== null}
          onChange={(e) => onJson(e.target.value)}
        />
        {jsonError && <ErrorBanner message={jsonError} className="mt-2" />}
      </details>
    </>
  );
}

function RuleBlock({
  index,
  rule,
  onChange,
  onRemove,
}: Readonly<{ index: number; rule: ACLRule; onChange: (patch: Partial<ACLRule>) => void; onRemove: () => void }>) {
  const n = index + 1;
  const extras = rule.actions.filter((a) => !(STANDARD_ACTIONS as readonly string[]).includes(a));
  const actions = [...STANDARD_ACTIONS, ...extras];

  function toggle(action: string, on: boolean) {
    const without = rule.actions.filter((a) => a !== action);
    onChange({ actions: on ? [...without, action] : without });
  }

  return (
    <div className="border border-ink-200 p-3 flex flex-col gap-2.5">
      <div className="flex items-center gap-1.5">
        <label className="sr-only" htmlFor={`rule-${n}-bucket`}>
          Bucket for rule {n}
        </label>
        <input
          id={`rule-${n}-bucket`}
          className="input-mono flex-1"
          placeholder="bucket name or *"
          autoComplete="off"
          value={rule.buckets.join(',')}
          onChange={(e) => onChange({ buckets: e.target.value.split(',') })}
        />
        <IconButton icon="x" label="Remove rule" onClick={onRemove} />
      </div>
      <div className="grid grid-cols-2 gap-x-4 gap-y-1.5">
        {actions.map((a) => (
          <label key={a} className="chk">
            <input type="checkbox" checked={rule.actions.includes(a)} onChange={(e) => toggle(a, e.target.checked)} />
            <span className="font-mono">{shortAction(a)}</span>
          </label>
        ))}
      </div>
    </div>
  );
}
