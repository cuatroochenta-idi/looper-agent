import { createEffect, createMemo, createResource, createSignal, For, onCleanup, Show } from "solid-js";
import { useNavigate, useParams } from "@solidjs/router";
import { runsStore, STATUS_PILLS, type StatusFilter } from "../lib/state/runsStore";
import { buildForest, groupBySession } from "../lib/state/runTree";
import { api } from "../lib/api";
import type { RunDetail } from "../lib/api/types";
import { Input } from "../components/ui/Input";
import { Spinner } from "../components/ui/Spinner";
import { EmptyState } from "../components/ui/EmptyState";
import { Badge, STATUS_LABEL, STATUS_TONE } from "../components/ui/Badge";
import { CopyButton } from "../components/ui/CopyButton";
import { cn } from "../components/ui/cn";
import { RunTree } from "../components/domain/RunTree";
import { RunStatusDot } from "../components/domain/RunStatusDot";
import { TraceTree, type LiveBuffer } from "../components/domain/TraceTree";
import { CostChip } from "../components/domain/CostChip";
import { ModelChip } from "../components/domain/ModelChip";
import { SubagentChip } from "../components/domain/SubagentChip";
import { TokenStats } from "../components/domain/TokenStats";
import { clock, duration, shortId, tokens, usd } from "../lib/format";

export function Runs() {
  const params = useParams();
  const navigate = useNavigate();
  const store = runsStore;

  const forest = createMemo(() => buildForest(store.runs() ?? []));
  const groups = createMemo(() => groupBySession(forest()));
  const total = createMemo(() => (store.runs() ?? []).length);

  return (
    <div class="grid grid-cols-1 gap-4 lg:grid-cols-[380px_minmax(0,1fr)] fade-up">
      {/* ---- sidebar ---- */}
      <aside class="flex max-h-[calc(100vh-100px)] flex-col rounded-[12px] border border-line bg-card lg:sticky lg:top-[76px]">
        <div class="border-b border-line p-3">
          <div class="relative">
            <Input
              placeholder="Filter by input or ID…"
              value={store.query()}
              onInput={(e) => store.setSearch(e.currentTarget.value)}
              class="pr-7"
            />
            <Show when={store.query()}>
              <button
                class="absolute right-2 top-1/2 -translate-y-1/2 text-[14px] text-faint hover:text-text"
                onClick={() => store.setSearch("")}
                aria-label="clear"
              >
                ×
              </button>
            </Show>
          </div>
          <div class="mt-2.5 flex flex-wrap gap-1">
            <For each={STATUS_PILLS}>
              {(p) => (
                <button
                  onClick={() => store.setStatusFilter(p.id as StatusFilter)}
                  class={cn(
                    "rounded-[7px] border px-2 py-1 text-[11.5px] font-medium transition-colors",
                    store.statusFilter() === p.id
                      ? "border-accent-line bg-accent-soft text-accent"
                      : "border-line bg-bg-raised text-muted hover:text-text",
                  )}
                >
                  {p.label}
                </button>
              )}
            </For>
          </div>
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto p-2.5">
          <Show
            when={!store.runs.loading || store.runs()}
            fallback={<div class="flex justify-center py-10"><Spinner /></div>}
          >
            <Show
              when={total() > 0}
              fallback={
                <EmptyState
                  title={
                    store.query()
                      ? `No matches for "${store.query()}".`
                      : store.statusFilter()
                        ? `No "${STATUS_PILLS.find((p) => p.id === store.statusFilter())?.label}" runs.`
                        : "No runs yet."
                  }
                />
              }
            >
              <div class="flex flex-col gap-2.5">
                <For each={groups()}>
                  {(g) => (
                    <details open class="group">
                      <summary class="mb-1.5 flex cursor-pointer list-none items-center gap-2 px-1 text-[11px] text-faint">
                        <span class="inline-block w-2.5 transition-transform group-open:rotate-90">▶</span>
                        <span class="font-mono uppercase tracking-wide">
                          {g.session_id === "unsessioned" ? "unsessioned" : `session ${shortId(g.session_id)}`}
                        </span>
                        <span class="text-line-strong">·</span>
                        <span class="font-mono">{g.nodes.length} run{g.nodes.length === 1 ? "" : "s"}</span>
                        <CostChip value={g.self_usd} class="!text-[11px]" />
                        <Show when={g.running}>
                          <span class="ml-auto flex items-center gap-1 text-info">
                            <span class="h-1.5 w-1.5 rounded-full bg-info pulse" /> live
                          </span>
                        </Show>
                      </summary>
                      <RunTree nodes={g.nodes} selectedId={params.id} onSelect={(id) => navigate(`/runs/${id}`)} />
                    </details>
                  )}
                </For>
              </div>
            </Show>
          </Show>
        </div>
      </aside>

      {/* ---- detail ---- */}
      <section class="min-h-[calc(100vh-100px)] rounded-[12px] border border-line bg-bg-raised">
        <Show
          when={params.id}
          fallback={
            <div class="relative flex h-full min-h-[420px] items-center justify-center overflow-hidden">
              <div class="bg-grid pointer-events-none absolute inset-0" />
              <EmptyState title="No run selected." hint="Pick one on the left." />
            </div>
          }
        >
          <RunDetailPane id={params.id!} onOpenRun={(rid) => navigate(`/runs/${rid}`)} />
        </Show>
      </section>
    </div>
  );
}

function RunDetailPane(props: { id: string; onOpenRun: (id: string) => void }) {
  const [detail, { refetch, mutate }] = createResource(() => props.id, api.getRun);
  const [live, setLive] = createSignal<LiveBuffer | undefined>();

  // Per-run detail SSE: live chunk buffer + persisted-step invalidation.
  createEffect(() => {
    const id = props.id;
    setLive(undefined);
    const stop = api.subscribe([`run:${id}`], {
      chunk: (e) => {
        if (e.run_id !== id) return;
        setLive((prev) => {
          const base =
            prev && prev.turn === e.turn ? prev : { turn: e.turn, text: "", reasoning: "" };
          return e.kind === "reasoning"
            ? { ...base, reasoning: base.reasoning + e.delta }
            : { ...base, text: base.text + e.delta };
        });
      },
      step_appended: () => void refetch(),
      run_updated: (u) => {
        // Patch header cheaply; clear live buffer once the run settles.
        mutate((prev) => (prev ? { ...prev, status: u.status, self_usd: u.self_usd, subtree_usd: u.subtree_usd, turns: u.turns } : prev));
        if (u.status !== "running") {
          setLive(undefined);
          void refetch();
        }
      },
    });
    onCleanup(stop);
  });

  return (
    <Show
      when={detail()}
      fallback={
        <div class="flex h-full min-h-[420px] items-center justify-center">
          <Show when={detail.error} fallback={<Spinner />}>
            <EmptyState icon="⚠" title="Could not load run." hint={String(detail.error)} />
          </Show>
        </div>
      }
    >
      {(d) => <DetailBody d={d()} live={live()} onOpenRun={props.onOpenRun} />}
    </Show>
  );
}

function DetailBody(props: { d: RunDetail; live?: LiveBuffer; onOpenRun: (id: string) => void }) {
  const d = () => props.d;
  const hasSubs = () => d().subagent_count > 0;
  const latency = () => duration(d().started_at, d().ended_at);

  return (
    <div class="flex flex-col">
      {/* sticky header */}
      <div class="sticky top-[64px] z-10 border-b border-line bg-bg-raised/85 px-5 py-3.5 backdrop-blur-md">
        <div class="flex flex-wrap items-center gap-2">
          <Badge tone={STATUS_TONE[d().status]}>
            <RunStatusDot status={d().status} size={7} class="mr-1" />
            {STATUS_LABEL[d().status]}
          </Badge>
          <Show when={d().kind === "subagent"}>
            <Badge tone="accent">sub-agent</Badge>
          </Show>
          <CopyButton value={d().id} label={`run ${shortId(d().id)}`} />
          <Show when={d().project}>
            <span class="text-[11.5px] text-faint">{d().project}</span>
          </Show>
          <span class="ml-auto font-mono text-[11px] text-faint">
            started {clock(d().started_at, true)} · {latency()} duration
          </span>
        </div>

        <p class="mt-2 line-clamp-2 text-[13px] leading-snug text-text/90">{d().input_preview}</p>

        {/* metric row */}
        <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[11.5px]">
          <Metric label="turns" value={<span class="font-mono tabular-nums">{d().turns}</span>} />
          <Metric
            label={hasSubs() ? "cost · total" : "cost"}
            value={<CostChip value={d().subtree_usd} estimated={d().cost_estimated} />}
          />
          <Metric label="input" value={<span class="font-mono tabular-nums text-muted">{tokens(d().input_tokens)}</span>} />
          <Metric label="output" value={<span class="font-mono tabular-nums text-muted">{tokens(d().output_tokens)}</span>} />
          <Show when={d().cached_tokens > 0}>
            <Metric label="cached" value={<span class="font-mono tabular-nums text-muted">{tokens(d().cached_tokens)}</span>} />
          </Show>
          <Metric label="latency" value={<span class="font-mono tabular-nums text-muted">{latency()}</span>} />
          <Show when={d().fallback_calls > 0}>
            <Metric label="fallback" value={<span class="font-mono tabular-nums text-warning">{d().fallback_calls} calls</span>} />
          </Show>
        </div>

        {/* full token breakdown incl. cache-write (billed 1.25×) */}
        <TokenStats
          class="mt-2"
          compact
          input={d().input_tokens}
          output={d().output_tokens}
          cached={d().cached_tokens}
          cacheWrite={d().cache_write_tokens}
        />

        <Show when={hasSubs()}>
          <div class="mt-2 flex flex-wrap items-center gap-2 text-[11px] text-muted">
            <SubagentChip count={d().subagent_count} running={d().subagents_running} />
            <span class="text-faint">
              incl. {d().subagent_count} sub-agent{d().subagent_count === 1 ? "" : "s"}: +
              {usd(d().subtree_usd - d().self_usd, d().cost_estimated)} · self{" "}
              <CostChip value={d().self_usd} estimated={d().cost_estimated} tone="muted" class="!text-[11px]" />
            </span>
          </div>
        </Show>
      </div>

      {/* body */}
      <div class="flex flex-col gap-4 px-5 py-4 pb-32">
        {/* providers table */}
        <Show when={d().providers.length > 0}>
          <div>
            <div class="mb-1.5 text-[11px] font-medium uppercase tracking-wide text-faint">
              models · {d().providers.length}
            </div>
            <div class="overflow-x-auto rounded-[10px] border border-line">
              <table class="w-full border-collapse text-[12px]">
                <thead>
                  <tr class="border-b border-line bg-sunken/60 text-left text-[10.5px] uppercase tracking-wide text-faint">
                    <th class="px-3 py-1.5 font-medium">provider</th>
                    <th class="px-3 py-1.5 font-medium">model</th>
                    <th class="px-3 py-1.5 text-right font-medium">calls</th>
                    <th class="px-3 py-1.5 text-right font-medium">in</th>
                    <th class="px-3 py-1.5 text-right font-medium">out</th>
                    <th class="px-3 py-1.5 text-right font-medium">cost</th>
                  </tr>
                </thead>
                <tbody>
                  <For each={d().providers}>
                    {(p) => (
                      <tr class="border-b border-line/60 last:border-0">
                        <td class="px-3 py-1.5 font-mono text-muted">{p.provider}</td>
                        <td class="px-3 py-1.5 font-mono text-text/85">
                          {p.model}
                          <Show when={p.fallback}>
                            <span class="ml-1.5 text-warning" title="fallback">↪ fallback</span>
                          </Show>
                        </td>
                        <td class="px-3 py-1.5 text-right font-mono tabular-nums text-muted">{p.calls}</td>
                        <td class="px-3 py-1.5 text-right font-mono tabular-nums text-muted">{tokens(p.input_tokens)}</td>
                        <td class="px-3 py-1.5 text-right font-mono tabular-nums text-muted">{tokens(p.output_tokens)}</td>
                        <td class="px-3 py-1.5 text-right">
                          <Show when={p.usd > 0 || p.estimated} fallback={<span class="text-faint">—</span>}>
                            <CostChip value={p.usd} estimated={p.estimated} />
                          </Show>
                        </td>
                      </tr>
                    )}
                  </For>
                </tbody>
              </table>
            </div>
          </div>
        </Show>

        {/* trace */}
        <div>
          <div class="mb-2 flex items-center gap-2">
            <div class="text-[11px] font-medium uppercase tracking-wide text-faint">
              trace · {d().turns_detail.length} turn{d().turns_detail.length === 1 ? "" : "s"}
            </div>
            <Show when={d().models[0]}>
              <ModelChip model={d().models[0]!} class="ml-1" />
            </Show>
          </div>
          <TraceTree detail={d()} live={props.live} onOpenRun={props.onOpenRun} />
        </div>
      </div>
    </div>
  );
}

function Metric(props: { label: string; value: import("solid-js").JSX.Element }) {
  return (
    <span class="inline-flex items-baseline gap-1.5">
      <span class="text-faint">{props.label}</span>
      {props.value}
    </span>
  );
}
