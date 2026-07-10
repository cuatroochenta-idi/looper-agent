import { Show, type JSX } from "solid-js";
import { cn } from "./cn";

export interface EmptyStateProps {
  icon?: JSX.Element;
  title: JSX.Element;
  hint?: JSX.Element;
  class?: string;
}

/** Quiet, centered placeholder. Legacy used the ∅ glyph — kept as default. */
export function EmptyState(props: EmptyStateProps) {
  return (
    <div class={cn("flex flex-col items-center justify-center px-6 py-14 text-center", props.class)}>
      <div class="mb-3 text-[26px] leading-none text-faint/70">{props.icon ?? "∅"}</div>
      <div class="text-[13px] font-medium text-muted">{props.title}</div>
      <Show when={props.hint}>
        <div class="mt-1 text-[12px] text-faint">{props.hint}</div>
      </Show>
    </div>
  );
}
