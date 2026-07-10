import type {
  ApiClient,
  ChatMessage,
  ChatSummary,
  CostsResponse,
  MeResponse,
  RunDetail,
  RunListItem,
  SseHandlers,
  Summary,
} from "./types";
import { openSse } from "./sse";

export class UnauthorizedError extends Error {
  constructor() {
    super("unauthorized");
    this.name = "UnauthorizedError";
  }
}

let redirecting = false;
function toLogin() {
  if (redirecting) return;
  redirecting = true;
  const here = window.location.pathname + window.location.search;
  if (!here.startsWith("/login")) {
    window.location.assign(`/login?next=${encodeURIComponent(here)}`);
  }
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    credentials: "include",
    headers: { Accept: "application/json", ...(init?.headers ?? {}) },
    ...init,
  });
  if (res.status === 401) {
    toLogin();
    throw new UnauthorizedError();
  }
  if (!res.ok) {
    let detail = "";
    try {
      detail = await res.text();
    } catch {
      /* ignore */
    }
    throw new Error(`${res.status} ${res.statusText}${detail ? `: ${detail}` : ""}`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

function qs(params: Record<string, string | undefined>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") sp.set(k, v);
  }
  const s = sp.toString();
  return s ? `?${s}` : "";
}

export const httpClient: ApiClient = {
  getSummary: (since) => req<Summary>(`/api/state/summary${qs({ since })}`),

  getRuns: async ({ since, status, q }) => {
    const data = await req<{ runs: RunListItem[] }>(`/api/state/runs${qs({ since, status, q })}`);
    return data.runs ?? [];
  },

  getRun: (id) => req<RunDetail>(`/api/state/runs/${encodeURIComponent(id)}`),

  getChats: async (since) => {
    const data = await req<{ chats: ChatSummary[] }>(`/api/state/chats${qs({ since })}`);
    return data.chats ?? [];
  },

  getChat: (key, since) =>
    req<{ chat: ChatSummary; messages: ChatMessage[] }>(
      `/api/state/chats/${encodeURIComponent(key)}${qs({ since })}`,
    ),

  getCosts: (since) => req<CostsResponse>(`/api/state/costs${qs({ since })}`),

  createRun: (input) =>
    req<{ id: string }>(`/api/run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ input }),
    }),

  login: (password, username) =>
    req<void>(`/api/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(username ? { username, password } : { password }),
    }),

  logout: () => req<void>(`/api/logout`, { method: "POST" }),

  me: () => req<MeResponse>(`/api/me`),

  subscribe: (topics: string[], handlers: SseHandlers) => openSse(topics, handlers),
};
