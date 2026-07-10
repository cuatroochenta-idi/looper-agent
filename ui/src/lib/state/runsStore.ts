import { createResource, createRoot, createSignal } from "solid-js";
import { api } from "../api";
import type { RunListItem } from "../api/types";
import { range, sinceParam } from "./timeRange";
import { debounce, sseHub } from "./sseHub";

export type StatusFilter = "" | "running" | "completed" | "errored" | "unknown";

// Filter pills mirror the legacy panel: all / running / done / failed / unknown.
export const STATUS_PILLS: { id: StatusFilter; label: string }[] = [
  { id: "", label: "all" },
  { id: "running", label: "running" },
  { id: "completed", label: "done" },
  { id: "errored", label: "failed" },
  { id: "unknown", label: "unknown" },
];

function createRunsStore() {
  const [statusFilter, setStatusFilter] = createSignal<StatusFilter>("");
  const [query, setQuery] = createSignal("");
  const [debouncedQuery, setDebouncedQuery] = createSignal("");
  const pushQuery = debounce((v: string) => setDebouncedQuery(v), 200);

  const setSearch = (v: string) => {
    setQuery(v);
    pushQuery(v);
  };

  const key = () => ({
    since: sinceParam(range()),
    status: statusFilter(),
    q: debouncedQuery(),
  });

  const [runs, { refetch, mutate }] = createResource(key, (k) =>
    api.getRuns({ since: k.since, status: k.status || undefined, q: k.q || undefined }),
  );

  // Coarse invalidation, debounced. Granular run_updated patches rows in place.
  sseHub.onRunsChanged(debounce(() => void refetch(), 250));
  sseHub.onRunUpdated((e) => {
    mutate((prev) => {
      if (!prev) return prev;
      let touched = false;
      const next = prev.map((r): RunListItem => {
        if (r.id !== e.id) return r;
        touched = true;
        return {
          ...r,
          status: e.status,
          last_seen_at: e.last_seen_at,
          self_usd: e.self_usd,
          subtree_usd: e.subtree_usd,
          turns: e.turns,
          tokens: e.tokens,
        };
      });
      // A row we don't have yet: let the coarse refetch bring it in.
      return touched ? next : prev;
    });
  });

  return {
    runs,
    refetch,
    statusFilter,
    setStatusFilter,
    query,
    setSearch,
  };
}

export const runsStore = createRoot(createRunsStore);
