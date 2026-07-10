import type {
  ApiClient,
  ChatMessage,
  ChatSummary,
  CostsResponse,
  MeResponse,
  ModelCost,
  RunDetail,
  RunListItem,
  SseHandlers,
  Summary,
} from "../types";
import { ALL_DETAILS, STREAM_RUN_ID, STREAM_TEXT, toListItem } from "./fixtures";

const delay = (ms: number) => new Promise((r) => setTimeout(r, ms));

function detailById(id: string): RunDetail | undefined {
  return ALL_DETAILS.find((r) => r.id === id);
}

function sinceCutoff(since: string): number {
  // since is a duration hint ("15m","1h","24h","all") or empty.
  if (!since || since === "all") return 0;
  const m = /^(\d+)(m|h|d)$/.exec(since);
  if (!m) return 0;
  const n = Number(m[1]);
  const unit = m[2] === "m" ? 60_000 : m[2] === "h" ? 3_600_000 : 86_400_000;
  return Date.now() - n * unit;
}

function topLevel(): RunListItem[] {
  return ALL_DETAILS.filter((r) => r.kind === "run" || isOrphan(r)).map(toListItem);
}

function isOrphan(r: RunDetail): boolean {
  return r.kind === "subagent" && !!r.parent_run_id && !detailById(r.parent_run_id);
}

function computeSummary(items: RunListItem[]): Summary {
  const s: Summary = {
    total_runs: items.length,
    running: 0,
    completed: 0,
    errored: 0,
    unknown: 0,
    total_usd: 0,
    cost_estimated: false,
    total_tokens: 0,
    avg_turns: 0,
  };
  // Sum self costs only across the whole set (never double count).
  let turnsTotal = 0;
  for (const r of ALL_DETAILS) {
    s.total_usd += r.self_usd;
    if (r.cost_estimated) s.cost_estimated = true;
  }
  for (const r of items) {
    s[r.status] += 1;
    s.total_tokens += r.tokens;
    turnsTotal += r.turns;
  }
  s.avg_turns = items.length ? Math.round((turnsTotal / items.length) * 10) / 10 : 0;
  return s;
}

function computeCosts(): CostsResponse {
  const byKey = new Map<string, ModelCost>();
  let total = 0;
  let estimated = false;
  for (const r of ALL_DETAILS) {
    for (const p of r.providers) {
      const key = `${p.provider}/${p.model}`;
      const acc = byKey.get(key) ?? {
        provider: p.provider,
        model: p.model,
        calls: 0,
        input_tokens: 0,
        output_tokens: 0,
        cached_tokens: 0,
        cache_write_tokens: 0,
        usd: 0,
        estimated: false,
      };
      acc.calls += p.calls;
      acc.input_tokens += p.input_tokens;
      acc.output_tokens += p.output_tokens;
      acc.cached_tokens += p.cached_tokens;
      acc.cache_write_tokens += p.cache_write_tokens;
      acc.usd += p.usd;
      acc.estimated = acc.estimated || p.estimated;
      byKey.set(key, acc);
      total += p.usd;
      estimated = estimated || p.estimated;
    }
  }
  return {
    total_usd: Math.round(total * 1e6) / 1e6,
    cost_estimated: estimated,
    by_model: [...byKey.values()].sort((a, b) => b.usd - a.usd),
  };
}

function chatMessages(root: RunDetail): ChatMessage[] {
  const streaming = root.id === STREAM_RUN_ID;
  // While streaming, leave the persisted content empty so the live chunk
  // buffer is the sole source (mirrors the server never persisting chunks).
  const agentContent = streaming
    ? ""
    : root.output || (root.error ? `Failed: ${root.error}` : "");
  return [
    {
      run_id: root.id,
      role: "user",
      content: root.input,
      status: "completed",
      streaming: false,
      subagent_count: 0,
      subagents_running: 0,
      usd: 0,
      cost_estimated: false,
      at: root.started_at,
    },
    {
      run_id: root.id,
      role: "agent",
      content: agentContent,
      status: root.status,
      streaming,
      subagent_count: root.subagent_count,
      subagents_running: root.subagents_running,
      usd: root.subtree_usd,
      cost_estimated: root.cost_estimated,
      at: root.last_seen_at,
    },
  ];
}

function chatSummaries(): ChatSummary[] {
  return ALL_DETAILS.filter((r) => r.kind === "run" || isOrphan(r)).map((r) => ({
    key: r.id,
    title: r.input_preview,
    project: r.project,
    started_at: r.started_at,
    last_seen_at: r.last_seen_at,
    message_count: 2,
    total_usd: r.subtree_usd,
    cost_estimated: r.cost_estimated,
    running: r.status === "running",
  }));
}

export const mockClient: ApiClient = {
  async getSummary(since) {
    await delay(120);
    const cut = sinceCutoff(since);
    const items = topLevel().filter((r) => Date.parse(r.last_seen_at) >= cut);
    return computeSummary(items);
  },

  async getRuns({ since, status, q }) {
    await delay(140);
    const cut = sinceCutoff(since);
    let runs = ALL_DETAILS.map(toListItem).filter((r) => Date.parse(r.last_seen_at) >= cut);
    if (status) runs = runs.filter((r) => r.status === status);
    if (q) {
      const needle = q.toLowerCase();
      runs = runs.filter(
        (r) => r.input_preview.toLowerCase().includes(needle) || r.id.toLowerCase().includes(needle),
      );
    }
    return runs;
  },

  async getRun(id) {
    await delay(100);
    const r = detailById(id);
    if (!r) throw new Error(`404: run ${id} not found`);
    return r;
  },

  async getChats(since) {
    await delay(120);
    const cut = sinceCutoff(since);
    return chatSummaries().filter((c) => Date.parse(c.last_seen_at) >= cut);
  },

  async getChat(key, _since) {
    await delay(120);
    const r = detailById(key);
    if (!r) throw new Error(`404: chat ${key} not found`);
    const [chat] = chatSummaries().filter((c) => c.key === key);
    return { chat: chat!, messages: chatMessages(r) };
  },

  async getCosts(_since) {
    await delay(120);
    return computeCosts();
  },

  async createRun(input) {
    await delay(80);
    return { id: `run_new-${Math.random().toString(36).slice(2, 8)}-${input.length}` };
  },

  async login() {
    await delay(200);
  },

  async logout() {
    await delay(80);
  },

  async me(): Promise<MeResponse> {
    await delay(60);
    // Mock env runs unauthenticated with auth disabled.
    return { auth_enabled: false, authenticated: true, username: "dev" };
  },

  subscribe(topics: string[], handlers: SseHandlers) {
    let cancelled = false;
    handlers.onOpen?.();

    const timers: ReturnType<typeof setTimeout>[] = [];
    const intervals: ReturnType<typeof setInterval>[] = [];

    const wantsStream = topics.some((t) => t === `run:${STREAM_RUN_ID}`);

    // Live text streaming for the streaming run's detail subscribers.
    if (wantsStream && handlers.chunk) {
      const words = STREAM_TEXT.split(" ");
      let i = 0;
      const push = () => {
        if (cancelled || i >= words.length) return;
        handlers.chunk?.({
          run_id: STREAM_RUN_ID,
          turn: 1,
          kind: "text",
          delta: (i === 0 ? "" : " ") + words[i],
        });
        i += 1;
        timers.push(setTimeout(push, 90 + Math.random() * 90));
      };
      timers.push(setTimeout(push, 400));
    }

    // Periodic list/summary invalidation + a run_updated tick for the runner.
    if (topics.includes("runs") || topics.includes("summary")) {
      intervals.push(
        setInterval(() => {
          if (cancelled) return;
          handlers.run_updated?.({
            id: STREAM_RUN_ID,
            kind: "run",
            status: "running",
            last_seen_at: new Date().toISOString(),
            self_usd: 0.0089,
            subtree_usd: 0.0089,
            turns: 1,
            tokens: 4200,
          });
        }, 4000),
      );
    }
    if (topics.includes("chats")) {
      intervals.push(
        setInterval(() => {
          if (!cancelled) handlers.chats_changed?.({});
        }, 8000),
      );
    }

    return () => {
      cancelled = true;
      timers.forEach(clearTimeout);
      intervals.forEach(clearInterval);
    };
  },
};
