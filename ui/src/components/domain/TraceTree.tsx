import { createResource, For, Show, type JSX } from "solid-js";
import { cn } from "../ui/cn";
import { TraceNode } from "./TraceNode";
import { ToolCallNode } from "./ToolCallNode";
import { ModelChip } from "./ModelChip";
import { CostChip } from "./CostChip";
import { SubagentChip } from "./SubagentChip";
import { Badge } from "../ui/Badge";
import { Spinner } from "../ui/Spinner";
import { EmptyState } from "../ui/EmptyState";
import { api } from "../../lib/api";
import type { RunDetail, Turn } from "../../lib/api/types";
import { duration, tokens } from "../../lib/format";

export interface LiveBuffer {
  turn: number;
  text: string;
  reasoning: string;
}

export function TraceTree(props: {
  detail: RunDetail;
  live?: LiveBuffer;
  depth?: number;
  onOpenRun?: (id: string) => void;
}) {
  const depth = props.depth ?? 0;
  const d = () => props.detail;

  return (
    <div class={cn("flex flex-col gap-1.5")}>
      {/* system prompt */}
      <Show when={d().system_prompt}>
        <TraceNode
          kind="system_prompt"
          tone="muted"
          preview={<span>{d().system_prompt!.length} chars · loaded at start</span>}
        >
          <p class="whitespace-pre-wrap text-[12px] leading-relaxed text-muted">{d().system_prompt}</p>
        </TraceNode>
      </Show>

      {/* user input */}
      <TraceNode
        kind="user"
        tone="accent"
        defaultOpen={depth === 0}
        preview={<span>{d().input_preview || "user input"}</span>}
      >
        <p class="whitespace-pre-wrap text-[12.5px] leading-relaxed text-text/90">{d().input}</p>
      </TraceNode>

      {/* turns */}
      <For each={d().turns_detail}>
        {(turn, i) => (
          <TurnBlock
            turn={turn}
            isLast={i() === d().turns_detail.length - 1}
            live={props.live?.turn === turn.turn ? props.live : undefined}
            onOpenRun={props.onOpenRun}
            depth={depth}
          />
        )}
      </For>

      <Show when={d().turns_detail.length === 0}>
        <EmptyState icon="…" title="Awaiting first step…" class="!py-8" />
      </Show>

      {/* run-level final / error */}
      <Show when={d().error}>
        <TraceNode kind="error" tone="danger" defaultOpen preview={<span>run failed</span>}>
          <p class="whitespace-pre-wrap font-mono text-[12px] leading-relaxed text-danger">{d().error}</p>
        </TraceNode>
      </Show>
      <Show when={!d().error && d().output && d().status === "completed"}>
        <TraceNode kind="final" tone="success" defaultOpen={depth === 0} preview={<span>produced a final response</span>}>
          <p class="whitespace-pre-wrap text-[12.5px] leading-relaxed text-text/90">{d().output}</p>
        </TraceNode>
      </Show>
    </div>
  );
}

function TurnBlock(props: {
  turn: Turn;
  isLast: boolean;
  live?: LiveBuffer;
  depth: number;
  onOpenRun?: (id: string) => void;
}) {
  const t = () => props.turn;
  const liveText = () => props.live?.text ?? "";
  const liveReasoning = () => props.live?.reasoning ?? "";
  const streaming = () => !t().ended_at;

  const assistantText = () => (t().assistant_text ?? "") + liveText();
  const reasoningText = () => (t().reasoning ?? "") + liveReasoning();

  return (
    <div class="rounded-[10px] border border-accent-line/40 bg-accent-soft/20 p-1.5">
      <div class="mb-1.5 flex items-center gap-2 px-1">
        <Badge tone="accent" mono>turn {t().turn}</Badge>
        <ModelChip model={`${t().provider}/${t().model}`} fallback={t().fallback} />
        <Show when={t().api_key_suffix}>
          <span class="font-mono text-[10.5px] text-faint">{t().api_key_suffix}</span>
        </Show>
        <Show when={t().fallback}>
          <Badge tone="warning" class="shrink-0">↪ fallback</Badge>
        </Show>
        <span class="ml-auto font-mono text-[10.5px] text-faint">
          <Show when={t().ended_at} fallback={<span class="text-info">running…</span>}>
            {duration(t().started_at, t().ended_at)}
          </Show>
        </span>
        <Show when={t().usage}>
          <span class="font-mono text-[10.5px] text-faint">
            {tokens((t().usage!.input_tokens ?? 0) + (t().usage!.output_tokens ?? 0))} tk
          </span>
        </Show>
      </div>

      <div class="flex flex-col gap-1.5">
        {/* reasoning (collapsible, dashed) */}
        <Show when={reasoningText()}>
          <TraceNode
            kind="reasoning"
            tone="muted"
            class="border-dashed"
            live={streaming() && !!liveReasoning()}
            preview={<span>{streaming() && liveReasoning() ? "thinking…" : `${reasoningText().length} chars`}</span>}
          >
            <p class="whitespace-pre-wrap text-[12px] italic leading-relaxed text-muted">{reasoningText()}</p>
          </TraceNode>
        </Show>

        {/* llm output */}
        <Show when={assistantText() || streaming()}>
          <TraceNode
            kind="llm_call"
            tone="info"
            defaultOpen
            collapsible
            live={streaming()}
            preview={
              <span>{assistantText() ? "model output" : "awaiting model response…"}</span>
            }
          >
            <p class={cn("whitespace-pre-wrap text-[12.5px] leading-relaxed text-text/90", streaming() && "caret")}>
              {assistantText() || <span class="text-faint">awaiting model response…</span>}
            </p>
          </TraceNode>
        </Show>

        {/* tool calls */}
        <For each={t().tool_calls}>
          {(call) => (
            <ToolCallNode call={call}>
              <For each={call.spawned_run_ids}>
                {(rid) => <SubagentInline runId={rid} depth={props.depth + 1} onOpenRun={props.onOpenRun} />}
              </For>
            </ToolCallNode>
          )}
        </For>

        {/* per-turn final / error */}
        <Show when={t().final}>
          <TraceNode kind="final" tone="success" defaultOpen preview={<span>produced a final response</span>}>
            <p class="whitespace-pre-wrap text-[12.5px] leading-relaxed text-text/90">{t().final}</p>
          </TraceNode>
        </Show>
        <Show when={t().error}>
          <TraceNode kind="error" tone="danger" defaultOpen preview={<span>turn errored</span>}>
            <p class="whitespace-pre-wrap font-mono text-[12px] leading-relaxed text-danger">{t().error}</p>
          </TraceNode>
        </Show>
      </div>
    </div>
  );
}

/** Lazily fetches a spawned subagent's detail and renders its trace inline. */
function SubagentInline(props: { runId: string; depth: number; onOpenRun?: (id: string) => void }): JSX.Element {
  const [detail] = createResource(() => props.runId, api.getRun);

  return (
    <div class="rounded-[10px] border border-accent-line/50 bg-accent-soft/15">
      <div class="flex items-center gap-2 border-b border-line px-2.5 py-1.5">
        <span class="text-accent" aria-hidden="true">↳</span>
        <span class="text-[11.5px] font-medium text-accent">spawned sub-agent run</span>
        <Show when={detail()}>
          {(d) => (
            <span class="flex items-center gap-2 text-[10.5px] text-faint">
              <span class="font-mono">{d().turns} turns</span>
              <CostChip value={d().subtree_usd} estimated={d().cost_estimated} class="!text-[11px]" />
              <SubagentChip count={d().subagent_count} running={d().subagents_running} />
            </span>
          )}
        </Show>
        <button
          class="ml-auto text-[11px] text-muted hover:text-accent"
          onClick={() => props.onOpenRun?.(props.runId)}
        >
          open full ↗
        </button>
      </div>
      <div class="p-2">
        <Show
          when={detail()}
          fallback={
            <div class="flex items-center gap-2 px-1 py-2 text-[11.5px] text-faint">
              <Spinner size={12} /> loading sub-agent trace…
            </div>
          }
        >
          {(d) => <TraceTree detail={d()} depth={props.depth} onOpenRun={props.onOpenRun} />}
        </Show>
      </div>
    </div>
  );
}
