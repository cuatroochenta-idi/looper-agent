// Typed mirror of docs/tasks/2026-07-10_api_contract.md.
// Field names are kept snake_case exactly as they arrive on the wire.

export type RunStatus = "running" | "completed" | "errored" | "unknown";
export type RunKind = "run" | "subagent";
export type ChatRole = "user" | "agent";

export interface Summary {
  total_runs: number;
  running: number;
  completed: number;
  errored: number;
  unknown: number;
  total_usd: number;
  cost_estimated: boolean;
  total_tokens: number;
  avg_turns: number;
}

export interface RunListItem {
  id: string;
  session_id: string;
  parent_run_id?: string;
  parent_tool_call_id?: string;
  kind: RunKind;
  project?: string;
  input_preview: string;
  output_preview?: string;
  status: RunStatus;
  turns: number;
  started_at: string;
  ended_at?: string;
  last_seen_at: string;
  self_usd: number;
  subtree_usd: number;
  cost_estimated: boolean;
  tokens: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_write_tokens: number;
  subagent_count: number;
  subagents_running: number;
  models: string[];
  fallback_calls: number;
}

export interface Usage {
  input_tokens?: number;
  output_tokens?: number;
  cached_tokens?: number;
  cache_write_tokens?: number;
  reasoning_tokens?: number;
}

export interface ProviderStat {
  provider: string;
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_write_tokens: number;
  usd: number;
  estimated: boolean;
  fallback?: boolean;
}

export interface ToolResult {
  content: string;
  is_error: boolean;
  at: string;
}

export interface ToolCall {
  id: string;
  name: string;
  args_json: string;
  result?: ToolResult;
  spawned_run_ids: string[];
}

export interface Turn {
  turn: number;
  provider: string;
  model: string;
  fallback: boolean;
  api_key_suffix?: string;
  assistant_text?: string;
  reasoning?: string;
  usage?: Usage;
  tool_calls: ToolCall[];
  final?: string;
  error?: string;
  started_at: string;
  ended_at?: string;
}

export interface RunDetail extends RunListItem {
  system_prompt?: string;
  input: string;
  output: string;
  error?: string;
  providers: ProviderStat[];
  turns_detail: Turn[];
  child_ids: string[];
}

export interface ChatSummary {
  key: string;
  title: string;
  project?: string;
  started_at: string;
  last_seen_at: string;
  message_count: number;
  total_usd: number;
  cost_estimated: boolean;
  running: boolean;
}

export interface ChatMessage {
  run_id: string;
  role: ChatRole;
  content: string;
  status: RunStatus;
  streaming: boolean;
  subagent_count: number;
  subagents_running: number;
  usd: number;
  cost_estimated: boolean;
  at: string;
}

export interface ModelCost {
  provider: string;
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_write_tokens: number;
  usd: number;
  estimated: boolean;
}

export interface CostsResponse {
  total_usd: number;
  cost_estimated: boolean;
  by_model: ModelCost[];
}

export interface MeResponse {
  auth_enabled: boolean;
  authenticated: boolean;
  username?: string;
}

// ---- SSE event payloads -------------------------------------------------

export interface RunUpdatedEvent {
  id: string;
  parent_run_id?: string;
  kind: RunKind;
  status: RunStatus;
  last_seen_at: string;
  self_usd: number;
  subtree_usd: number;
  turns: number;
  tokens: number;
}

export interface StepAppendedEvent {
  run_id: string;
  step: {
    kind: string;
    turn: number;
    content?: string;
    tool_name?: string;
    tool_call_id?: string;
    err?: string;
    at: string;
    usage?: Usage;
    provider?: string;
    model?: string;
  };
}

export interface ChunkEvent {
  run_id: string;
  turn: number;
  kind: "text" | "reasoning";
  delta: string;
}

export type SseEventMap = {
  runs_changed: Record<string, never>;
  run_updated: RunUpdatedEvent;
  step_appended: StepAppendedEvent;
  chunk: ChunkEvent;
  chats_changed: Record<string, never>;
};

export type SseEventName = keyof SseEventMap;

// ---- Client interface (real + mock share this) --------------------------

export interface ApiClient {
  getSummary(since: string): Promise<Summary>;
  getRuns(params: { since: string; status?: string; q?: string }): Promise<RunListItem[]>;
  getRun(id: string): Promise<RunDetail>;
  getChats(since: string): Promise<ChatSummary[]>;
  getChat(key: string, since: string): Promise<{ chat: ChatSummary; messages: ChatMessage[] }>;
  getCosts(since: string): Promise<CostsResponse>;
  createRun(input: string): Promise<{ id: string }>;
  login(password: string, username?: string): Promise<void>;
  logout(): Promise<void>;
  me(): Promise<MeResponse>;
  /** Subscribe to SSE topics; returns an unsubscribe fn. */
  subscribe(topics: string[], handlers: SseHandlers): () => void;
}

export type SseHandlers = {
  [K in SseEventName]?: (data: SseEventMap[K]) => void;
} & { onOpen?: () => void; onError?: () => void };
