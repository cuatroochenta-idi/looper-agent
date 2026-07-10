import { createMemo, For, Show } from "solid-js";
import { useNavigate } from "@solidjs/router";
import { StatTile } from "../components/ui/StatTile";
import { Card } from "../components/ui/Card";
import { Spinner } from "../components/ui/Spinner";
import { EmptyState } from "../components/ui/EmptyState";
import { RunCard } from "../components/domain/RunCard";
import { summaryStore } from "../lib/state/summaryStore";
import { runsStore } from "../lib/state/runsStore";
import { buildForest } from "../lib/state/runTree";
import { tokens, usd } from "../lib/format";

export function Dashboard() {
  const navigate = useNavigate();
  const s = summaryStore.summary;

  const recent = createMemo(() => {
    const runs = runsStore.runs() ?? [];
    return buildForest(runs)
      .map((n) => n.run)
      .slice(0, 6);
  });

  return (
    <div class="flex flex-col gap-5 fade-up">
      <div>
        <h1 class="text-[17px] font-semibold tracking-tight text-text">Dashboard</h1>
        <p class="mt-0.5 text-[12.5px] text-muted">Top-level runs in the selected window. Sub-agent spend rolls into its parent.</p>
      </div>

      {/* stat tiles */}
      <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile
          label="total runs"
          tone="accent"
          value={<Show when={s()} fallback={<Skel />}>{s()!.total_runs}</Show>}
          hint={
            <Show when={s()}>
              <span class="flex items-center gap-2 text-[11px]">
                <span class="text-info">{s()!.running} running</span>
                <span class="text-success">{s()!.completed} done</span>
                <Show when={s()!.errored > 0}><span class="text-danger">{s()!.errored} failed</span></Show>
              </span>
            </Show>
          }
        />
        <StatTile
          label="total cost"
          tone="success"
          value={<Show when={s()} fallback={<Skel />}>{usd(s()!.total_usd, s()!.cost_estimated)}</Show>}
          hint={<Show when={s()?.cost_estimated}><span class="text-warning">~ includes estimates</span></Show>}
        />
        <StatTile
          label="tokens"
          value={<Show when={s()} fallback={<Skel />}>{tokens(s()!.total_tokens)}</Show>}
          hint="input + output + cache"
        />
        <StatTile
          label="avg turns"
          value={<Show when={s()} fallback={<Skel />}>{s()!.avg_turns}</Show>}
          hint="per top-level run"
        />
      </div>

      {/* recent runs */}
      <Card class="overflow-hidden">
        <div class="flex items-center justify-between border-b border-line px-4 py-2.5">
          <h2 class="text-[13px] font-semibold text-text">Recent runs</h2>
          <span class="flex items-center gap-1.5 text-[11px] text-faint">
            <span class="h-1.5 w-1.5 rounded-full bg-success pulse" /> live · SSE
          </span>
        </div>
        <Show
          when={!runsStore.runs.loading || runsStore.runs()}
          fallback={<div class="flex justify-center py-10"><Spinner /></div>}
        >
          <Show when={recent().length > 0} fallback={<EmptyState title="No runs yet." hint="Kick off a run to see it here." />}>
            <div class="flex flex-col gap-2 p-3">
              <For each={recent()}>
                {(run) => <RunCard run={run} onClick={() => navigate(`/runs/${run.id}`)} />}
              </For>
            </div>
          </Show>
        </Show>
      </Card>
    </div>
  );
}

function Skel() {
  return <span class="inline-block h-5 w-12 animate-pulse rounded bg-input align-middle" />;
}
