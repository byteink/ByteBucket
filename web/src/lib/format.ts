// Shared number and date formatting so every page renders sizes, counts and
// timestamps the same way.

// formatBytes renders a byte count in the largest binary unit that keeps the
// number readable, mirroring how operators think about storage size.
export function formatBytes(n: number): string {
  if (n < 1024) return `${Math.round(n)} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

export function formatCount(n: number): string {
  return Math.round(n).toLocaleString('en-US');
}

// formatDate renders an RFC3339 (or Date-parsable) timestamp as YYYY-MM-DD.
// Go's zero time (year 0001) and unparsable input render as an em dash so
// legacy records read cleanly instead of showing a bogus date.
export function formatDate(ts?: string): string {
  if (!ts) return '—';
  const d = new Date(ts);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '—';
  return d.toISOString().slice(0, 10);
}

// formatDateTime renders a timestamp as a short local date and time
// ("Sep 11, 14:02"). Seconds are included when `seconds` is set (logs).
export function formatDateTime(ts: string | number, seconds = false): string {
  const d = typeof ts === 'number' ? new Date(ts * 1000) : new Date(ts);
  if (Number.isNaN(d.getTime())) return String(ts);
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    ...(seconds ? { second: '2-digit' } : {}),
  });
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
