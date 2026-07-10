import {
  createEffect,
  createMemo,
  createResource,
  createSignal,
  For,
  onCleanup,
  Show,
} from "solid-js";
import { useNavigate, useParams } from "@solidjs/router";
import { chatsStore } from "../lib/state/chatsStore";
import { range, sinceParam } from "../lib/state/timeRange";
import { api } from "../lib/api";
import type { ChatMessage } from "../lib/api/types";
import { Input } from "../components/ui/Input";
import { Spinner } from "../components/ui/Spinner";
import { EmptyState } from "../components/ui/EmptyState";
import { cn } from "../components/ui/cn";
import { CostChip } from "../components/domain/CostChip";
import { SubagentChip } from "../components/domain/SubagentChip";
import { RunStatusDot } from "../components/domain/RunStatusDot";
import { TraceTree, type LiveBuffer } from "../components/domain/TraceTree";
import { clock, relative, usd } from "../lib/format";

export function Chats() {
  const params = useParams();
  const navigate = useNavigate();
  const [query, setQuery] = createSignal("");
  const [traceRun, setTraceRun] = createSignal<string | undefined>();

  const chats = createMemo(() => {
    const list = chatsStore.chats() ?? [];
    const q = query().toLowerCase();
    return q ? list.filter((c) => c.title.toLowerCase().includes(q) || c.key.toLowerCase().includes(q)) : list;
  });

  // Reset the trace panel when the selected conversation changes.
  createEffect(() => {
    params.key;
    setTraceRun(undefined);
  });

  return (
    <div class="flex h-[calc(100vh-100px)] gap-4 fade-up">
      {/* conversation list */}
      <aside class="flex w-[280px] shrink-0 flex-col rounded-[12px] border border-line bg-card">
        <div class="border-b border-line p-3">
          <Input placeholder="Filter…" value={query()} onInput={(e) => setQuery(e.currentTarget.value)} />
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto p-2">
          <Show
            when={!chatsStore.chats.loading || chatsStore.chats()}
            fallback={<div class="flex justify-center py-10"><Spinner /></div>}
          >
            <Show
              when={chats().length > 0}
              fallback={<EmptyState title={query() ? `No matches for "${query()}".` : "No chats yet."} />}
            >
              <div class="flex flex-col gap-1">
                <For each={chats()}>
                  {(c) => (
                    <button
                      onClick={() => navigate(`/chats/${c.key}`)}
                      class={cn(
                        "flex flex-col gap-1 rounded-[9px] border px-2.5 py-2 text-left transition-colors",
                        params.key === c.key
                          ? "border-accent-line bg-accent-soft/40"
                          : "border-transparent hover:border-line hover:bg-card-hover",
                      )}
                    >
                      <div class="flex items-center gap-1.5">
                        <RunStatusDot status={c.running ? "running" : "completed"} size={7} />
                        <span class="min-w-0 flex-1 truncate text-[12.5px] text-text/90">{c.title}</span>
                      </div>
                      <div class="flex items-center gap-2 text-[11px] text-faint">
                        <span class="font-mono">{c.message_count} turn{c.message_count === 1 ? "" : "s"}</span>
                        <CostChip value={c.total_usd} estimated={c.cost_estimated} class="!text-[11px]" />
                        <span class="ml-auto">{relative(c.last_seen_at)}</span>
                      </div>
                    </button>
                  )}
                </For>
              </div>
            </Show>
          </Show>
        </div>
      </aside>

      {/* thread */}
      <section class="flex min-w-0 flex-1 flex-col rounded-[12px] border border-line bg-bg-raised">
        <Show
          when={params.key}
          fallback={
            <div class="relative flex flex-1 items-center justify-center overflow-hidden">
              <div class="bg-grid pointer-events-none absolute inset-0" />
              <EmptyState title="No conversation selected." hint="Pick one on the left." />
            </div>
          }
        >
          <Thread key={params.key!} onOpenTrace={setTraceRun} activeTrace={traceRun()} />
        </Show>
      </section>

      {/* sliding trace panel */}
      <Show when={traceRun()}>
        {(rid) => (
          <aside class="flex w-[min(46%,560px)] min-w-[360px] shrink-0 flex-col rounded-[12px] border border-line bg-card fade-up">
            <div class="flex items-center gap-2 border-b border-line px-3.5 py-2.5">
              <span class="text-[12px] font-semibold text-text">Trace</span>
              <span class="font-mono text-[11px] text-faint">{rid().slice(0, 10)}</span>
              <button
                class="ml-auto text-[16px] leading-none text-faint hover:text-text"
                onClick={() => setTraceRun(undefined)}
                aria-label="Close detail panel"
              >
                ×
              </button>
            </div>
            <div class="min-h-0 flex-1 overflow-y-auto p-3">
              <TracePanel id={rid()} onOpenTrace={setTraceRun} />
            </div>
          </aside>
        )}
      </Show>
    </div>
  );
}

function Thread(props: { key: string; onOpenTrace: (id: string) => void; activeTrace?: string }) {
  const [data, { refetch, mutate }] = createResource(
    () => ({ key: props.key, since: sinceParam(range()) }),
    (k) => api.getChat(k.key, k.since),
  );
  const [live, setLive] = createSignal<string>("");

  // Stream the last agent message live via its run's chunk events.
  createEffect(() => {
    const d = data();
    if (!d) return;
    const streamingMsg = d.messages.find((m) => m.role === "agent" && m.streaming);
    if (!streamingMsg) return;
    setLive("");
    const stop = api.subscribe([`run:${streamingMsg.run_id}`], {
      chunk: (e) => {
        if (e.kind === "text") setLive((t) => t + e.delta);
      },
      run_updated: (u) => {
        if (u.status !== "running") {
          setLive("");
          void refetch();
        }
      },
      step_appended: () => void refetch(),
    });
    onCleanup(stop);
  });

  const chatChanged = () => {
    // Coarse refetch also patches nothing else; mutate keeps identity stable.
    void refetch();
  };
  createEffect(() => {
    props.key;
    setLive("");
    mutate(undefined);
    chatChanged();
  });

  return (
    <Show when={data()} fallback={<div class="flex flex-1 items-center justify-center"><Spinner /></div>}>
      {(d) => (
        <>
          <div class="border-b border-line px-4 py-3">
            <h2 class="truncate text-[13.5px] font-semibold text-text">{d().chat.title}</h2>
            <div class="mt-0.5 flex items-center gap-2 text-[11.5px] text-faint">
              <Show when={d().chat.project}>
                <span>{d().chat.project}</span>
                <span class="text-line-strong">·</span>
              </Show>
              <span class="font-mono">{d().chat.message_count} messages</span>
              <span class="text-line-strong">·</span>
              <CostChip value={d().chat.total_usd} estimated={d().chat.cost_estimated} class="!text-[11.5px]" />
            </div>
          </div>
          <div class="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-4 pb-24">
            <For each={d().messages}>
              {(m) => (
                <Bubble
                  msg={m}
                  live={m.streaming ? live() : ""}
                  active={props.activeTrace === m.run_id}
                  onOpenTrace={() => props.onOpenTrace(m.run_id)}
                />
              )}
            </For>
          </div>
        </>
      )}
    </Show>
  );
}

function Bubble(props: { msg: ChatMessage; live: string; active: boolean; onOpenTrace: () => void }) {
  const m = () => props.msg;
  const isUser = () => m().role === "user";
  const content = () => m().content + (m().streaming ? props.live : "");
  const empty = () => !content() && !m().streaming;

  return (
    <div class={cn("flex", isUser() ? "justify-end" : "justify-start")}>
      <div
        onClick={props.onOpenTrace}
        class={cn(
          "group max-w-[76%] cursor-pointer rounded-[12px] border px-3.5 py-2.5 transition-colors",
          isUser()
            ? "rounded-br-[4px] border-accent-line/50 bg-accent-soft/50"
            : "rounded-bl-[4px] border-line bg-card hover:border-line-strong",
          m().status === "errored" && "border-danger/40 bg-danger-soft/40",
          props.active && "ring-2 ring-accent/40",
        )}
      >
        <div class="mb-1 flex items-center gap-1.5 text-[10.5px] uppercase tracking-wide text-faint">
          <span>{isUser() ? "you" : "agent"}</span>
          <Show when={m().streaming}>
            <span class="flex items-center gap-1 text-info"><Spinner size={9} /> thinking…</span>
          </Show>
        </div>

        <Show
          when={!empty()}
          fallback={<span class="text-[12.5px] italic text-faint">(no output)</span>}
        >
          <p class={cn("whitespace-pre-wrap text-[13px] leading-relaxed text-text/90", m().streaming && "caret")}>
            {content()}
          </p>
        </Show>

        <div class="mt-2 flex items-center gap-2 border-t border-line/60 pt-1.5 text-[10.5px] text-faint">
          <SubagentChip count={m().subagent_count} running={m().subagents_running} />
          <Show when={m().usd > 0}>
            <CostChip value={m().usd} estimated={m().cost_estimated} class="!text-[10.5px]" />
          </Show>
          <span class="ml-auto font-mono">{clock(m().at)}</span>
          <span class="text-accent opacity-0 transition-opacity group-hover:opacity-100">trace ↗</span>
        </div>
      </div>
    </div>
  );
}

function TracePanel(props: { id: string; onOpenTrace: (id: string) => void }) {
  const [detail, { refetch }] = createResource(() => props.id, api.getRun);
  const [live, setLive] = createSignal<LiveBuffer | undefined>();

  createEffect(() => {
    const id = props.id;
    setLive(undefined);
    const stop = api.subscribe([`run:${id}`], {
      chunk: (e) => {
        if (e.run_id !== id) return;
        setLive((prev) => {
          const base = prev && prev.turn === e.turn ? prev : { turn: e.turn, text: "", reasoning: "" };
          return e.kind === "reasoning"
            ? { ...base, reasoning: base.reasoning + e.delta }
            : { ...base, text: base.text + e.delta };
        });
      },
      run_updated: (u) => {
        if (u.status !== "running") {
          setLive(undefined);
          void refetch();
        }
      },
      step_appended: () => void refetch(),
    });
    onCleanup(stop);
  });

  return (
    <Show when={detail()} fallback={<div class="flex justify-center py-8"><Spinner /></div>}>
      {(d) => (
        <>
          <div class="mb-3 flex flex-wrap items-center gap-2 text-[11px]">
            <span class="font-mono text-muted">{d().turns} turns</span>
            <CostChip value={d().subtree_usd} estimated={d().cost_estimated} />
            <span class="text-faint">self {usd(d().self_usd, d().cost_estimated)}</span>
          </div>
          <TraceTree detail={d()} live={live()} onOpenRun={props.onOpenTrace} />
        </>
      )}
    </Show>
  );
}
