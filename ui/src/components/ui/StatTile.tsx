import { Show, type JSX } from "solid-js";
import { cn } from "./cn";

export interface StatTileProps {
  label: string;
  value: JSX.Element;
  hint?: JSX.Element;
  tone?: "default" | "accent" | "success" | "warning";
  class?: string;
}

const toneRing: Record<NonNullable<StatTileProps["tone"]>, string> = {
  default: "",
  accent: "before:bg-accent",
  success: "before:bg-success",
  warning: "before:bg-warning",
};

export function StatTile(props: StatTileProps) {
  return (
    <div
      class={cn(
        "relative overflow-hidden rounded-[12px] border border-line bg-card px-4 py-3.5 shadow-[var(--shadow-card)]",
        "before:absolute before:inset-y-0 before:left-0 before:w-[3px] before:content-['']",
        toneRing[props.tone ?? "default"],
        props.class,
      )}
    >
      <div class="text-[11px] font-medium uppercase tracking-wide text-faint">{props.label}</div>
      <div class="mt-1.5 font-mono text-[22px] font-medium leading-none tabular-nums text-text">
        {props.value}
      </div>
      <Show when={props.hint}>
        <div class="mt-1.5 text-[11.5px] text-muted">{props.hint}</div>
      </Show>
    </div>
  );
}
