import { createResource, createRoot } from "solid-js";
import { api } from "../api";
import { range, sinceParam } from "./timeRange";
import { debounce, sseHub } from "./sseHub";

function createSummaryStore() {
  const [summary, { refetch }] = createResource(() => sinceParam(range()), api.getSummary);

  const soft = debounce(() => void refetch(), 400);
  sseHub.onRunsChanged(soft);
  // run_updated moves tiles too (running counts, totals); debounce to stay calm.
  sseHub.onRunUpdated(soft);

  return { summary, refetch };
}

export const summaryStore = createRoot(createSummaryStore);
