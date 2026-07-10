import { Show } from "solid-js";
import { cn } from "../ui/cn";
import { Badge, STATUS_LABEL } from "../ui/Badge";
import { RunStatusDot } from "./RunStatusDot";
import { ModelChip } from "./ModelChip";
import { CostChip } from "./CostChip";
import { SubagentChip } from "./SubagentChip";
import { CopyButton } from "../ui/CopyButton";
import type { RunListItem } from "../../lib/api/types";
import { clock, shortId, tokens } from "../../lib/format";

const RAIL: Record<RunListItem["status"], string> = {
  running: "before:bg-info",
  completed: "before:bg-success",
  errored: "before:bg-danger",
  unknown: "before:bg-faint",
};

/**
 * A run row. Shows the subtree total (self + descendants) with an
 * "incl. N sub-agents" hint so cost attribution is never ambiguous.
 */
export function RunCard(props: {
  run: RunListItem;
  selected?: boolean;
  orphan?: boolean;
  onClick?: () => void;
  compact?: boolean;
}) {
  const r = () => props.run;
  const hasSubs = () => r().subagent_count > 0;

  return (
    <div
      onClick={props.onClick}
      role={props.onClick ? "button" : undefined}
      class={cn(
        "group relative overflow-hidden rounded-[10px] border bg-card pl-3.5 pr-3 py-2.5 transition-colors duration-150",
        "before:absolute before:inset-y-0 before:left-0 before:w-[3px] before:content-['']",
        RAIL[r().status],
        props.onClick && "cursor-pointer hover:bg-card-hover",
        props.selected ? "border-accent-line bg-card-hover" : "border-line hover:border-line-strong",
        r().status === "running" && "shadow-[0_0_0_1px_var(--info-soft)]",
      )}
    >
      {/* row 1 — status · model · id */}
      <div class="flex items-center gap-2">
        <RunStatusDot status={r().status} />
        <span
          class={cn(
            "text-[11px] font-medium uppercase tracking-wide",
            r().status === "running"
              ? "text-info"
              : r().status === "completed"
                ? "text-success"
                : r().status === "errored"
                  ? "text-danger"
                  : "text-faint",
          )}
        >
          {STATUS_LABEL[r().status]}
        </span>
        <Show when={r().models[0]}>
          <ModelChip model={r().models[0]!} fallback={r().fallback_calls > 0} />
        </Show>
        <Show when={props.orphan}>
          <Badge tone="warning" title="parent run not found — surfaced at top level">
            orphan
          </Badge>
        </Show>
        <span class="ml-auto">
          <CopyButton value={r().id} label={shortId(r().id)} />
        </span>
      </div>

      {/* row 2 — input preview */}
      <p
        class="mt-1.5 text-[12.5px] leading-snug text-text/90"
        style={{
          display: "-webkit-box",
          "-webkit-line-clamp": props.compact ? "1" : "2",
          "-webkit-box-orient": "vertical",
          overflow: "hidden",
        }}
      >
        {r().input_preview || "(no input)"}
      </p>

      {/* row 3 — metrics */}
      <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-muted">
        <span class="font-mono tabular-nums text-faint">{r().turns} turns</span>
        <span class="text-line-strong">·</span>
        <span class="inline-flex items-center gap-1">
          <CostChip value={r().subtree_usd} estimated={r().cost_estimated} />
          <Show when={hasSubs()}>
            <span class="text-faint">total</span>
          </Show>
        </span>
        <span class="text-line-strong">·</span>
        <span class="font-mono tabular-nums text-faint">{tokens(r().tokens)} tk</span>
        <span class="text-line-strong">·</span>
        <span class="font-mono tabular-nums text-faint">{clock(r().started_at)}</span>
      </div>

      <Show when={hasSubs()}>
        <div class="mt-2 flex items-center gap-2">
          <SubagentChip count={r().subagent_count} running={r().subagents_running} />
          <span class="text-[11px] text-faint">
            incl. {r().subagent_count} · self <CostChip value={r().self_usd} estimated={r().cost_estimated} tone="muted" class="!text-[11px]" />
          </span>
        </div>
      </Show>
    </div>
  );
}
