import { createSignal, Show, type JSX } from "solid-js";
import { cn } from "../ui/cn";
import { Badge, type BadgeTone } from "../ui/Badge";

export interface TraceNodeProps {
  kind: string;
  tone: BadgeTone;
  preview?: JSX.Element;
  right?: JSX.Element;
  children?: JSX.Element;
  defaultOpen?: boolean;
  collapsible?: boolean;
  live?: boolean;
  class?: string;
}

/** One node in the turn-by-turn trace: kind badge + preview + collapsible body. */
export function TraceNode(props: TraceNodeProps) {
  const collapsible = props.collapsible ?? !!props.children;
  const [open, setOpen] = createSignal(props.defaultOpen ?? false);

  return (
    <div class={cn("rounded-[9px] border border-line bg-card/60", props.class)}>
      <button
        type="button"
        disabled={!collapsible}
        onClick={() => collapsible && setOpen((v) => !v)}
        class={cn(
          "flex w-full items-center gap-2 px-2.5 py-1.5 text-left",
          collapsible && "hover:bg-card-hover/60",
        )}
      >
        <Show when={collapsible}>
          <span
            class={cn(
              "inline-block w-3 shrink-0 text-[10px] text-faint transition-transform duration-150",
              open() && "rotate-90",
            )}
          >
            ▶
          </span>
        </Show>
        <Badge tone={props.tone} mono class="shrink-0">
          {props.kind}
        </Badge>
        <Show when={props.live}>
          <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-info pulse" title="streaming" />
        </Show>
        <Show when={props.preview}>
          <span class="min-w-0 flex-1 truncate text-[12px] text-muted">{props.preview}</span>
        </Show>
        <Show when={props.right}>
          <span class="ml-auto shrink-0">{props.right}</span>
        </Show>
      </button>
      <Show when={collapsible && open()}>
        <div class="border-t border-line px-2.5 py-2 fade-up">{props.children}</div>
      </Show>
    </div>
  );
}
