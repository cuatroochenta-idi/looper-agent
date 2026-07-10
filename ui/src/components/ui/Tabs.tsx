import { For, type JSX } from "solid-js";
import { cn } from "./cn";

export interface TabItem<T extends string> {
  id: T;
  label: JSX.Element;
  count?: number;
}

export interface TabsProps<T extends string> {
  items: TabItem<T>[];
  value: T;
  onChange: (id: T) => void;
  size?: "sm" | "md";
  class?: string;
}

/** Pill-style segmented control (nav + filter pills share this look). */
export function Tabs<T extends string>(props: TabsProps<T>) {
  return (
    <div
      class={cn(
        "inline-flex items-center gap-0.5 rounded-[10px] border border-line bg-bg-raised p-0.5",
        props.class,
      )}
      role="tablist"
    >
      <For each={props.items}>
        {(item) => {
          const active = () => props.value === item.id;
          return (
            <button
              role="tab"
              aria-selected={active()}
              onClick={() => props.onChange(item.id)}
              class={cn(
                "inline-flex items-center gap-1.5 rounded-[8px] font-medium transition-colors duration-150",
                props.size === "sm" ? "h-6 px-2 text-[11.5px]" : "h-7 px-3 text-[12.5px]",
                active()
                  ? "bg-accent-soft text-accent shadow-[inset_0_0_0_1px_var(--accent-line)]"
                  : "text-muted hover:text-text hover:bg-input/60",
              )}
            >
              <span>{item.label}</span>
              {item.count !== undefined && (
                <span
                  class={cn(
                    "font-mono text-[10.5px] tabular-nums",
                    active() ? "text-accent/80" : "text-faint",
                  )}
                >
                  {item.count}
                </span>
              )}
            </button>
          );
        }}
      </For>
    </div>
  );
}
