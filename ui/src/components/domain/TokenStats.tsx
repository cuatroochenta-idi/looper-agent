import { For, Show } from "solid-js";
import { cn } from "../ui/cn";
import { tokens } from "../../lib/format";

interface Field {
  label: string;
  value: number;
  hint: string;
}

/** in / out / cached / cache-write token breakdown as a tight metric row. */
export function TokenStats(props: {
  input: number;
  output: number;
  cached: number;
  cacheWrite: number;
  class?: string;
  compact?: boolean;
}) {
  const fields = (): Field[] => {
    const f: Field[] = [
      { label: "in", value: props.input, hint: "input tokens" },
      { label: "out", value: props.output, hint: "output tokens" },
    ];
    if (props.cached > 0) f.push({ label: "cached", value: props.cached, hint: "cache reads" });
    if (props.cacheWrite > 0)
      f.push({ label: "cache-write", value: props.cacheWrite, hint: "cache writes (billed 1.25×)" });
    return f;
  };

  return (
    <div class={cn("flex flex-wrap items-center gap-x-3 gap-y-1", props.class)}>
      <For each={fields()}>
        {(f) => (
          <span class="inline-flex items-baseline gap-1" title={f.hint}>
            <span class={cn("text-faint", props.compact ? "text-[10px]" : "text-[11px]")}>{f.label}</span>
            <span
              class={cn(
                "font-mono tabular-nums text-muted",
                props.compact ? "text-[11px]" : "text-[12px]",
              )}
            >
              {tokens(f.value)}
            </span>
          </span>
        )}
      </For>
      <Show when={fields().length === 2 && props.cached === 0}>
        <span class="text-[10px] text-faint/70">no cache</span>
      </Show>
    </div>
  );
}
