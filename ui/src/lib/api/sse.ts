import type { SseHandlers, SseEventName } from "./types";

const KNOWN_EVENTS: SseEventName[] = [
  "runs_changed",
  "run_updated",
  "step_appended",
  "chunk",
  "chats_changed",
];

/**
 * EventSource wrapper: one multiplexed connection per topic-set, typed
 * dispatch, and automatic reconnect with capped exponential backoff.
 * Returns an unsubscribe fn. Every event is safe to drop (server never
 * persists chunks) so a reconnect just resumes; callers refetch snapshots.
 */
export function openSse(topics: string[], handlers: SseHandlers): () => void {
  let es: EventSource | null = null;
  let closed = false;
  let attempt = 0;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;

  const url = `/api/events?topics=${encodeURIComponent(topics.join(","))}`;

  const connect = () => {
    if (closed) return;
    es = new EventSource(url, { withCredentials: true });

    es.onopen = () => {
      attempt = 0;
      handlers.onOpen?.();
    };

    for (const name of KNOWN_EVENTS) {
      es.addEventListener(name, (ev) => {
        const fn = handlers[name];
        if (!fn) return;
        try {
          const data = (ev as MessageEvent).data ? JSON.parse((ev as MessageEvent).data) : {};
          // Cast is safe: handler key and payload are correlated by SseEventMap.
          (fn as (d: unknown) => void)(data);
        } catch {
          /* malformed frame — drop it, snapshot refetch will heal */
        }
      });
    }

    es.onerror = () => {
      handlers.onError?.();
      es?.close();
      es = null;
      if (closed) return;
      // Capped backoff: 0.5s, 1s, 2s, 4s … max 15s, with jitter.
      const delay = Math.min(15000, 500 * 2 ** attempt) + Math.random() * 300;
      attempt += 1;
      reconnectTimer = setTimeout(connect, delay);
    };
  };

  connect();

  return () => {
    closed = true;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    es?.close();
    es = null;
  };
}
