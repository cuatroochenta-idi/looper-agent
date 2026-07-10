import { createSignal } from "solid-js";

// The `since` value all stores respect. Presets serialize to a duration hint
// the server understands ("15m", "1h", "24h", "" = all). A custom range
// serializes its start bound to an RFC3339 timestamp.

export type RangePreset = "15m" | "1h" | "24h" | "all" | "custom";

export interface TimeRange {
  preset: RangePreset;
  /** Present only when preset === "custom". */
  fromISO?: string;
  toISO?: string;
}

const [range, setRange] = createSignal<TimeRange>({ preset: "1h" });

export { range, setRange };

export const PRESETS: { id: RangePreset; label: string }[] = [
  { id: "15m", label: "15 min" },
  { id: "1h", label: "1 hour" },
  { id: "24h", label: "24 hours" },
  { id: "all", label: "all" },
];

/** Serialize the active range to the `since` query value. */
export function sinceParam(r: TimeRange = range()): string {
  switch (r.preset) {
    case "15m":
      return "15m";
    case "1h":
      return "1h";
    case "24h":
      return "24h";
    case "all":
      return "all";
    case "custom":
      return r.fromISO ?? "all";
  }
}

export function setPreset(preset: RangePreset) {
  setRange({ preset });
}

export function setCustom(fromISO: string, toISO?: string) {
  setRange({ preset: "custom", fromISO, toISO });
}
