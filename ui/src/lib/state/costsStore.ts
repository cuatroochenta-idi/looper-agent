import { createResource, createRoot } from "solid-js";
import { api } from "../api";
import { range, sinceParam } from "./timeRange";
import { debounce, sseHub } from "./sseHub";

function createCostsStore() {
  const [costs, { refetch }] = createResource(() => sinceParam(range()), api.getCosts);
  sseHub.onRunsChanged(debounce(() => void refetch(), 500));
  return { costs, refetch };
}

export const costsStore = createRoot(createCostsStore);
