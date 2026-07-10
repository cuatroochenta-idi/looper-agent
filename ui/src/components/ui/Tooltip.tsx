import { createSignal, Show, type JSX } from "solid-js";
import { cn } from "./cn";

export interface TooltipProps {
  content: JSX.Element;
  children: JSX.Element;
  class?: string;
}

/** Lightweight CSS-positioned tooltip; no portal needed for these short labels. */
export function Tooltip(props: TooltipProps) {
  const [open, setOpen] = createSignal(false);
  return (
    <span
      class={cn("relative inline-flex", props.class)}
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
      onFocusIn={() => setOpen(true)}
      onFocusOut={() => setOpen(false)}
    >
      {props.children}
      <Show when={open()}>
        <span
          role="tooltip"
          class="pointer-events-none absolute bottom-[calc(100%+6px)] left-1/2 z-50 -translate-x-1/2 whitespace-nowrap rounded-[7px] border border-line-strong bg-bg-raised px-2 py-1 text-[11px] text-text shadow-[var(--shadow-pop)] fade-up"
        >
          {props.content}
        </span>
      </Show>
    </span>
  );
}
