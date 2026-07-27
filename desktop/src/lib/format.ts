// How values are worded on screen.
//
// Times are the interesting case. A dashboard's job is to answer "is this
// current?", and "4 minutes ago" answers it where "2026-07-27T18:04:11Z" makes
// the reader do arithmetic. The absolute time is kept in a title attribute
// wherever that matters.

/** True for a timestamp the daemon left unset, including Go's zero time. */
function unset(iso: string | null | undefined): boolean {
  if (!iso) return true;
  const t = new Date(iso);
  return Number.isNaN(t.getTime()) || t.getUTCFullYear() <= 1;
}

/** "4 minutes ago", or a fallback for a timestamp that was never set. */
export function ago(iso: string | null | undefined, never = "never"): string {
  if (unset(iso)) return never;
  const then = new Date(iso as string).getTime();
  const seconds = Math.round((Date.now() - then) / 1000);
  if (seconds < 0) return "in " + span(-seconds);
  if (seconds < 45) return "just now";
  return span(seconds) + " ago";
}

/** "in 12 minutes", or a fallback when there is no such moment. */
export function until(iso: string | null | undefined, never = "never"): string {
  if (unset(iso)) return never;
  const seconds = Math.round((new Date(iso as string).getTime() - Date.now()) / 1000);
  if (seconds <= 0) return "now";
  return "in " + span(seconds);
}

/** The absolute time, for a tooltip beside a relative one. */
export function absolute(iso: string | null | undefined): string | undefined {
  if (unset(iso)) return undefined;
  return new Date(iso as string).toLocaleString();
}

/**
 * A duration in seconds as the largest unit that stays readable.
 *
 * Each pair is the divisor that leaves the unit it names: dividing seconds by 60
 * gives minutes, so the first pair is [60, "minute"].
 */
export function span(seconds: number): string {
  const units: Array<[number, string]> = [
    [60, "minute"],
    [60, "hour"],
    [24, "day"],
    [7, "week"],
    [4.35, "month"],
    [12, "year"],
  ];
  let value = seconds;
  let name = "second";
  for (const [size, next] of units) {
    if (value < size) break;
    value /= size;
    name = next;
  }
  const rounded = Math.round(value);
  return `${rounded} ${name}${rounded === 1 ? "" : "s"}`;
}

/** The gap between two timestamps, for a session's length. */
export function duration(startISO: string, endISO?: string | null): string {
  if (!endISO) return "still open";
  const ms = new Date(endISO).getTime() - new Date(startISO).getTime();
  if (!Number.isFinite(ms) || ms < 0) return "unknown";
  if (ms < 1000) return "under a second";
  return span(Math.round(ms / 1000));
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let value = n / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

/** A ref shortened the way git shortens a SHA, leaving branch names alone. */
export function shortRef(ref: string): string {
  return /^[0-9a-f]{40}$/i.test(ref) ? ref.slice(0, 8) : ref;
}
