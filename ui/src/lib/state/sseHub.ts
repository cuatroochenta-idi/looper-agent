import { api } from "../api";
import type { RunUpdatedEvent } from "../api/types";

// One always-on connection for coarse, list-level topics. Detail views open
// their own ephemeral `run:{id}` subscriptions. Stores register callbacks here
// instead of each opening a socket, honoring the "one multiplexed connection".

type Listeners = {
  runsChanged: Set<() => void>;
  chatsChanged: Set<() => void>;
  runUpdated: Set<(e: RunUpdatedEvent) => void>;
  open: Set<(v: boolean) => void>;
};

const listeners: Listeners = {
  runsChanged: new Set(),
  chatsChanged: new Set(),
  runUpdated: new Set(),
  open: new Set(),
};

let started = false;
let connected = false;

function setConnected(v: boolean) {
  connected = v;
  listeners.open.forEach((f) => f(v));
}

function ensureStarted() {
  if (started) return;
  started = true;
  api.subscribe(["runs", "chats", "summary"], {
    onOpen: () => setConnected(true),
    onError: () => setConnected(false),
    runs_changed: () => listeners.runsChanged.forEach((f) => f()),
    chats_changed: () => listeners.chatsChanged.forEach((f) => f()),
    run_updated: (e) => listeners.runUpdated.forEach((f) => f(e)),
  });
}

function on<K extends keyof Listeners>(
  key: K,
  fn: Listeners[K] extends Set<infer F> ? F : never,
): () => void {
  // Register before starting so a synchronous onOpen (mock client) is caught.
  const set = listeners[key] as Set<unknown>;
  set.add(fn);
  ensureStarted();
  return () => set.delete(fn);
}

export const sseHub = {
  onRunsChanged: (fn: () => void) => on("runsChanged", fn),
  onChatsChanged: (fn: () => void) => on("chatsChanged", fn),
  onRunUpdated: (fn: (e: RunUpdatedEvent) => void) => on("runUpdated", fn),
  onConnection: (fn: (open: boolean) => void) => {
    const off = on("open", fn);
    fn(connected); // replay current state — onOpen is a one-shot that may have already fired
    return off;
  },
};

/** Small debounce helper shared by stores for coarse refetch. */
export function debounce<T extends (...a: never[]) => void>(fn: T, ms: number): T {
  let t: ReturnType<typeof setTimeout> | undefined;
  return ((...args: never[]) => {
    if (t) clearTimeout(t);
    t = setTimeout(() => fn(...args), ms);
  }) as T;
}
