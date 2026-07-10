import { splitProps, type JSX } from "solid-js";
import { cn } from "./cn";
import type { RunStatus } from "../../lib/api/types";

export type BadgeTone =
  | "neutral"
  | "accent"
  | "success"
  | "warning"
  | "danger"
  | "info"
  | "muted";

const tones: Record<BadgeTone, string> = {
  neutral: "bg-input text-muted border-line",
  accent: "bg-accent-soft text-accent border-accent-line",
  success: "bg-success-soft text-success border-success/30",
  warning: "bg-warning-soft text-warning border-warning/30",
  danger: "bg-danger-soft text-danger border-danger/30",
  info: "bg-info-soft text-info border-info/30",
  muted: "bg-transparent text-faint border-line",
};

export interface BadgeProps extends JSX.HTMLAttributes<HTMLSpanElement> {
  tone?: BadgeTone;
  mono?: boolean;
}

export function Badge(props: BadgeProps) {
  const [local, rest] = splitProps(props, ["tone", "mono", "class", "children"]);
  return (
    <span
      {...rest}
      class={cn(
        "inline-flex items-center gap-1 rounded-[6px] border px-1.5 py-0.5 text-[10.5px] font-medium uppercase tracking-wide leading-none",
        local.mono && "font-mono tracking-normal normal-case",
        tones[local.tone ?? "neutral"],
        local.class,
      )}
    >
      {local.children}
    </span>
  );
}

export const STATUS_TONE: Record<RunStatus, BadgeTone> = {
  running: "info",
  completed: "success",
  errored: "danger",
  unknown: "muted",
};

/** Legacy label vocabulary: done / failed. */
export const STATUS_LABEL: Record<RunStatus, string> = {
  running: "running",
  completed: "done",
  errored: "failed",
  unknown: "unknown",
};
