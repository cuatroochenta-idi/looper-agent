import { Show } from "solid-js";
import { cn } from "../ui/cn";

/** "⤷ 3 sub-agents · 1 live" — subtree fan-out marker. */
export function SubagentChip(props: {
  count: number;
  running?: number;
  class?: string;
}) {
  return (
    <Show when={props.count > 0}>
      <span
        class={cn(
          "inline-flex items-center gap-1 rounded-[6px] border border-accent-line bg-accent-soft px-1.5 py-0.5 text-[11px] leading-none text-accent",
          props.class,
        )}
      >
        <span aria-hidden="true">⤷</span>
        <span class="font-mono tabular-nums">{props.count}</span>
        <span>{props.count === 1 ? "sub-agent" : "sub-agents"}</span>
        <Show when={(props.running ?? 0) > 0}>
          <span class="text-info">· {props.running} live</span>
        </Show>
      </span>
    </Show>
  );
}
