// Presentation formatters. Costs, tokens, durations, relative time.

/** USD to 4 decimals; "~" prefix marks an estimated (table-priced) figure. */
export function usd(value: number, estimated = false): string {
  const n = Number.isFinite(value) ? value : 0;
  return `${estimated ? "~" : ""}$${n.toFixed(4)}`;
}

/** Compact token count: 1.2k / 3.4M, exact under 1000. */
export function tokens(n: number): string {
  if (!Number.isFinite(n)) return "0";
  const abs = Math.abs(n);
  if (abs < 1000) return String(Math.round(n));
  if (abs < 1_000_000) return `${trim(n / 1000)}k`;
  if (abs < 1_000_000_000) return `${trim(n / 1_000_000)}M`;
  return `${trim(n / 1_000_000_000)}B`;
}

function trim(n: number): string {
  return (Math.round(n * 10) / 10).toFixed(1).replace(/\.0$/, "");
}

/** Human duration between two RFC3339 timestamps (or ms). */
export function duration(startISO: string, endISO?: string): string {
  const start = Date.parse(startISO);
  const end = endISO ? Date.parse(endISO) : Date.now();
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "—";
  return durationMs(Math.max(0, end - start));
}

export function durationMs(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${trim(s)}s`;
  const m = Math.floor(s / 60);
  const rem = Math.round(s % 60);
  if (m < 60) return rem ? `${m}m ${rem}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return mm ? `${h}h ${mm}m` : `${h}h`;
}

/** Clock time HH:MM:SS from a timestamp. */
export function clock(iso: string, withMillis = false): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const base = d.toLocaleTimeString("en-GB", { hour12: false });
  if (!withMillis) return base;
  return `${base}.${String(d.getMilliseconds()).padStart(3, "0")}`;
}

/** Relative time: "just now", "3m ago", "2h ago", "5d ago". */
export function relative(iso: string): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "—";
  const diff = Date.now() - t;
  const s = Math.round(diff / 1000);
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  return `${d}d ago`;
}

/** Short id pill text: first 8 chars. */
export function shortId(id: string): string {
  return id.length <= 10 ? id : id.slice(0, 8);
}

/** provider/model → { provider, model } split for chips. */
export function splitModel(ref: string): { provider: string; model: string } {
  const i = ref.indexOf("/");
  if (i < 0) return { provider: "", model: ref };
  return { provider: ref.slice(0, i), model: ref.slice(i + 1) };
}

/** Pretty-print a JSON string; return the raw string if it is not JSON. */
export function prettyJson(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}
