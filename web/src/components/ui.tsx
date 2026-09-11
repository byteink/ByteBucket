// Small shared primitives. Every page composes these instead of restating
// button/badge/tooltip markup, which is what keeps the surfaces coherent.
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Icon, type IconName } from './icons';
import { copyText } from '../lib/clipboard';

export type TipPos = 'above' | 'below' | 'right';

function tipClass(pos: TipPos, end = false): string {
  return `tt${pos === 'below' ? ' tt-below' : pos === 'right' ? ' tt-right' : ''}${end ? ' tt-end' : ''}`;
}

// Tip wraps any element with the CSS tooltip. The wrapper is inline-flex so
// it never changes the layout of what it wraps.
export function Tip({
  text,
  pos = 'above',
  end = false,
  className = '',
  children,
}: Readonly<{ text: string; pos?: TipPos; end?: boolean; className?: string; children: ReactNode }>) {
  return (
    <span className={`inline-flex ${tipClass(pos, end)} ${className}`} data-tip={text}>
      {children}
    </span>
  );
}

// IconButton is a 28px ghost button that always carries a tooltip and an
// accessible name, so icon-only actions are never a guessing game.
export function IconButton({
  icon,
  label,
  onClick,
  disabled = false,
  pos = 'above',
  className = '',
  type = 'button',
}: Readonly<{
  icon: IconName;
  label: string;
  onClick?: () => void;
  disabled?: boolean;
  pos?: TipPos;
  className?: string;
  type?: 'button' | 'submit';
}>) {
  return (
    <button
      type={type}
      className={`btn-ghost btn-ic ${tipClass(pos)} ${className}`}
      data-tip={label}
      aria-label={label}
      onClick={onClick}
      disabled={disabled}
    >
      <Icon name={icon} />
    </button>
  );
}

// CopyButton copies a value and confirms with a short-lived "Copied" tooltip.
export function CopyButton({
  value,
  label = 'Copy',
  onError,
}: Readonly<{ value: string; label?: string; onError?: (msg: string) => void }>) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);

  async function onClick() {
    try {
      await copyText(value);
      setCopied(true);
    } catch (e) {
      onError?.(e instanceof Error ? e.message : String(e));
    }
  }
  return (
    <button
      type="button"
      className={`btn shrink-0 tt${copied ? ' tt-on' : ''}`}
      data-tip={copied ? 'Copied' : label}
      aria-label={label}
      onClick={onClick}
    >
      <Icon name="copy" />
    </button>
  );
}

export function Seg<T extends string>({
  options,
  value,
  onChange,
  label,
}: Readonly<{ options: ReadonlyArray<{ key: T; label: string }>; value: T; onChange: (v: T) => void; label: string }>) {
  return (
    <div className="seg" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.key} type="button" aria-pressed={o.key === value} onClick={() => onChange(o.key)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Badge({ muted = false, children, tip }: Readonly<{ muted?: boolean; children: ReactNode; tip?: string }>) {
  const base = muted ? 'badge-muted' : 'badge';
  if (!tip) return <span className={base}>{children}</span>;
  return (
    <span className={`${base} ${tipClass('below')}`} data-tip={tip}>
      {children}
    </span>
  );
}

// Visibility renders the shared ACL vocabulary: Public draws the eye,
// Private stays quiet, inherited state is explained on hover.
export function Visibility({
  acl,
  source,
}: Readonly<{ acl: 'private' | 'public-read'; source?: 'object' | 'bucket' | 'default' }>) {
  if (acl !== 'public-read') return <span className="text-xs text-ink-500">Private</span>;
  if (source === 'bucket') {
    return (
      <Badge muted tip={'Inherited from the bucket ACL.\nSet an object ACL to override.'}>
        Public
      </Badge>
    );
  }
  return <Badge>Public</Badge>;
}

export function PageHeader({
  title,
  sub,
  actions,
  badge,
}: Readonly<{ title: ReactNode; sub?: ReactNode; actions?: ReactNode; badge?: ReactNode }>) {
  return (
    <div className="ph">
      <div>
        <h1>
          {title}
          {badge}
        </h1>
        {sub && <p>{sub}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

export function SearchInput({
  value,
  onChange,
  placeholder,
  label,
  className = '',
}: Readonly<{ value: string; onChange: (v: string) => void; placeholder: string; label: string; className?: string }>) {
  return (
    <div className={`search ${className}`}>
      <Icon name="search" />
      <input
        type="search"
        className="input"
        value={value}
        placeholder={placeholder}
        aria-label={label}
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  );
}

export function EmptyState({ text, action }: Readonly<{ text: string; action?: ReactNode }>) {
  return (
    <div className="empty">
      <p>{text}</p>
      {action}
    </div>
  );
}

export function Loading() {
  return <p className="text-ink-500 text-sm">Loading.</p>;
}

// Saved confirms a completed write next to its own Save button, so feedback
// sits where the action happened rather than in a banner at the top.
export function Saved({ text = 'Saved' }: Readonly<{ text?: string }>) {
  return (
    <span className="ok" role="status">
      <Icon name="check" size={14} />
      {text}
    </span>
  );
}

// useModal binds an open flag to a native <dialog>. showModal() gives focus
// trapping, Escape handling and a backdrop without any JS of our own.
function useModal(open: boolean, onClose: () => void) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && el.open) el.close();
  }, [open]);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const onCancel = (e: Event) => {
      e.preventDefault();
      onClose();
    };
    el.addEventListener('cancel', onCancel);
    return () => el.removeEventListener('cancel', onCancel);
  }, [onClose]);
  return ref;
}

export function Dialog({
  open,
  title,
  onClose,
  children,
  className = 'dlg',
}: Readonly<{ open: boolean; title: ReactNode; onClose: () => void; children: ReactNode; className?: string }>) {
  const ref = useModal(open, onClose);
  return (
    <dialog ref={ref} className={className} aria-label={typeof title === 'string' ? title : undefined}>
      {open && (
        <>
          <h2>{title}</h2>
          {children}
        </>
      )}
    </dialog>
  );
}

// ConfirmDialog replaces window.confirm: it names the target, states the
// consequence, and the primary button says what it does. An optional
// `match` string must be typed before the action enables.
export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  match,
  busy = false,
  error,
  onConfirm,
  onClose,
}: Readonly<{
  open: boolean;
  title: ReactNode;
  body: ReactNode;
  confirmLabel: string;
  match?: string;
  busy?: boolean;
  error?: string | null;
  onConfirm: () => void;
  onClose: () => void;
}>) {
  const [typed, setTyped] = useState('');
  useEffect(() => {
    if (!open) setTyped('');
  }, [open]);
  const ready = !busy && (match === undefined || typed === match);
  return (
    <Dialog open={open} title={title} onClose={onClose}>
      <p>{body}</p>
      {match !== undefined && (
        <div className="mt-4">
          <label className="field-label" htmlFor="confirm-match">
            Type the name to confirm
          </label>
          <input
            id="confirm-match"
            className="input-mono"
            value={typed}
            placeholder={match}
            autoComplete="off"
            onChange={(e) => setTyped(e.target.value)}
          />
        </div>
      )}
      {error && (
        <div role="alert" className="text-xs text-danger border-l-2 border-danger pl-3 mt-4">
          {error}
        </div>
      )}
      <div className="btns">
        <button type="button" className="btn" onClick={onClose} disabled={busy}>
          Cancel
        </button>
        <button type="button" className="btn-primary" onClick={onConfirm} disabled={!ready}>
          {busy ? 'Working' : confirmLabel}
        </button>
      </div>
    </Dialog>
  );
}

// PromptDialog replaces window.prompt for a single text value.
export function PromptDialog({
  open,
  title,
  body,
  label,
  initial,
  submitLabel,
  mono = true,
  busy = false,
  error,
  onSubmit,
  onClose,
}: Readonly<{
  open: boolean;
  title: ReactNode;
  body?: ReactNode;
  label: string;
  initial: string;
  submitLabel: string;
  mono?: boolean;
  busy?: boolean;
  error?: string | null;
  onSubmit: (value: string) => void;
  onClose: () => void;
}>) {
  const [value, setValue] = useState(initial);
  useEffect(() => {
    if (open) setValue(initial);
  }, [open, initial]);
  const ready = !busy && value.trim() !== '' && value !== initial;
  return (
    <Dialog open={open} title={title} onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (ready) onSubmit(value.trim());
        }}
      >
        {body && <p>{body}</p>}
        <div className="mt-4">
          <label className="field-label" htmlFor="prompt-value">
            {label}
          </label>
          <input
            id="prompt-value"
            className={mono ? 'input-mono' : 'input'}
            value={value}
            autoFocus
            onChange={(e) => setValue(e.target.value)}
          />
        </div>
        {error && (
          <div role="alert" className="text-xs text-danger border-l-2 border-danger pl-3 mt-4">
            {error}
          </div>
        )}
        <div className="btns">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn-primary" disabled={!ready}>
            {busy ? 'Working' : submitLabel}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

// Drawer is a right-side modal panel for editing one record in place.
export function Drawer({
  open,
  title,
  onClose,
  children,
  footer,
}: Readonly<{ open: boolean; title: ReactNode; onClose: () => void; children: ReactNode; footer: ReactNode }>) {
  const ref = useModal(open, onClose);
  return (
    <dialog ref={ref} className="drawer" aria-label={typeof title === 'string' ? title : undefined}>
      {open && (
        <>
          <div className="hd">
            <h2>{title}</h2>
            <IconButton icon="x" label="Close" onClick={onClose} />
          </div>
          <div className="bd">{children}</div>
          <div className="ft">{footer}</div>
        </>
      )}
    </dialog>
  );
}
