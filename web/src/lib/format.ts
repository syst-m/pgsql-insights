/* Formatting rules the UI shares (R10.21 and the number-formatting rules).
 *
 * R10.21: timestamps render in the browser's local zone **with the zone
 * labelled**, and the absolute UTC value is available on hover. A timestamp
 * rendered without its zone is the classic "is this 3am or 15am?" bug, so the
 * zone is part of the string, not a tooltip-only detail.
 *
 * The rest is the small set of formatters every page needs so a number is
 * never formatted three different ways in one screen.
 */

/** R10.21's resolved local zone id, e.g. "Europe/Dublin". Empty in environments
 *  where Intl cannot resolve one. */
export function localZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? "";
  } catch {
    return "";
  }
}

export interface TimeFormatOptions {
  /** Include the clock, not just the date. Default true. */
  withTime?: boolean;
  /** Append the zone label. Default true (R10.21). */
  withZone?: boolean;
}

/** Format an RFC 3339 UTC instant for display.
 *
 * Returns "—" for null/empty/unparseable rather than "Invalid Date": a missing
 * timestamp is a fact about the data, and rendering it as a date is a lie.
 */
export function formatTimestamp(value: string | null, opts: TimeFormatOptions = {}): string {
  const { withTime = true, withZone = true } = opts;
  const date = parseDate(value);
  if (date === null) return NULL_VALUE;

  const zone = localZone();
  const parts: Intl.DateTimeFormatPart[] = zone
    ? new Intl.DateTimeFormat("en-GB", {
        timeZone: zone,
        ...(withTime
          ? { year: "numeric", month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }
          : { year: "numeric", month: "short", day: "2-digit" }),
      }).formatToParts(date)
    : [];

  const get = (t: Intl.DateTimeFormatPartTypes): string => parts.find((p) => p.type === t)?.value ?? "";
  const datePart = `${get("day")} ${get("month")} ${get("year")}`;
  const timePart = withTime ? ` ${get("hour")}:${get("minute")}:${get("second")}` : "";
  const zonePart = withZone && zone !== "" ? ` ${shortZone(date, zone)}` : "";
  return `${datePart}${timePart}${zonePart}`;
}

/** The short zone abbreviation (GMT/IST here, PDT in June). Falls back to the
 *  numeric offset where a name is unavailable. */
function shortZone(date: Date, zone: string): string {
  const named = new Intl.DateTimeFormat("en-GB", { timeZone: zone, timeZoneName: "short" }).formatToParts(date);
  const t = named.find((p) => p.type === "timeZoneName");
  if (t !== undefined) return t.value;
  const offset = new Intl.DateTimeFormat("en-GB", { timeZone: zone, timeZoneName: "longOffset" }).formatToParts(date);
  return offset.find((p) => p.type === "timeZoneName")?.value ?? zone;
}

/** The absolute UTC string shown in a `title` attribute on hover (R10.21). */
export function utcTitle(value: string | null): string {
  const raw = value?.trim() ?? "";
  if (raw === "" || parseDate(raw) === null) return "no timestamp";
  // Echo the payload's own string. Re-serialising through Date appends a
  // `.000` milliseconds field the API never sent, so the hover would show a
  // value that is not the one the API returned.
  if (raw.endsWith("Z")) return `${raw} UTC`;
  return `${parseDate(raw)?.toISOString()} UTC`;
}

/** RFC 3339 in the shape the API emits (`2026-11-20T11:04:12Z`, offsets and
 *  fractional seconds allowed). `new Date()` alone accepts a lot the contract
 *  does not — "11/20/2026" parses in V8 — so the string is validated before it
 *  is trusted. */
const RFC3339 = /^\d{4}-\d{2}-\d{2}[Tt ]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$/;

export function parseDate(value: string | null): Date | null {
  if (value === null) return null;
  const raw = value.trim();
  if (raw === "" || !RFC3339.test(raw)) return null;
  const d = new Date(raw);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** What every cell renders when it has no value. An em dash, not a zero and
 *  not a blank cell — the two are distinguishable in a table and must stay so. */
export const NULL_VALUE = "—";

/** A plain number with thousands grouping. `null` renders as the em dash. */
export function formatNumber(value: number | null, digits = 0): string {
  if (value === null || !Number.isFinite(value)) return NULL_VALUE;
  return new Intl.NumberFormat("en-GB", {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(value);
}

/** A percentage from a 0-100 value, one decimal, e.g. "12.4%". */
export function formatPercent(value: number | null, digits = 1): string {
  if (value === null || !Number.isFinite(value)) return NULL_VALUE;
  return `${formatNumber(value, digits)}%`;
}

/** A duration in ms, switching units at the point where the extra digits stop
 *  meaning anything: 842 ms, 1.42 s, 1m 04s. */
export function formatDurationMs(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return NULL_VALUE;
  if (value < 1000) return `${formatNumber(value, value < 10 ? 1 : 0)} ms`;
  if (value < 60_000) return `${formatNumber(value / 1000, 2)} s`;
  const mins = Math.floor(value / 60_000);
  const secs = Math.round((value - mins * 60_000) / 1000);
  return `${mins}m ${String(secs).padStart(2, "0")}s`;
}

/** A byte count from a Kubernetes quantity string ("10Gi") or a number of bytes. */
export function formatSize(value: string | number | null): string {
  if (value === null || value === "") return NULL_VALUE;
  if (typeof value === "string") return value; // k8s quantities are already canonical
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let n = value;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i += 1;
  }
  return `${formatNumber(n, i === 0 ? 0 : 1)} ${units[i]}`;
}

/** Days until a date, for the cert-expiry column. Negative means expired. */
export function daysUntil(value: string | null, now: Date = new Date()): number | null {
  const date = parseDate(value);
  if (date === null) return null;
  return Math.floor((date.getTime() - now.getTime()) / 86_400_000);
}
