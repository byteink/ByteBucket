import type { IPBanConfig } from './admin';

// IP_BAN_LIMITS mirrors the server bounds (internal/middleware/ipban.go). The
// server is the gate; these only give fast inline feedback before Save.
export const IP_BAN_LIMITS = { maxFailures: 10000, windowSeconds: 3600, banSeconds: 604800 };

function inRange(v: number, max: number): boolean {
  return Number.isInteger(v) && v >= 1 && v <= max;
}

// ipBanError returns the first invalid field's message, or null when the
// config would be accepted. Bounds apply even when disabled, as on the server.
export function ipBanError(cfg: IPBanConfig): string | null {
  if (!inRange(cfg.maxFailures, IP_BAN_LIMITS.maxFailures)) {
    return `Max failures must be a whole number from 1 to ${IP_BAN_LIMITS.maxFailures}.`;
  }
  if (!inRange(cfg.windowSeconds, IP_BAN_LIMITS.windowSeconds)) {
    return `Window must be a whole number of seconds from 1 to ${IP_BAN_LIMITS.windowSeconds}.`;
  }
  if (!inRange(cfg.banSeconds, IP_BAN_LIMITS.banSeconds)) {
    return `Ban duration must be a whole number of seconds from 1 to ${IP_BAN_LIMITS.banSeconds}.`;
  }
  return null;
}
