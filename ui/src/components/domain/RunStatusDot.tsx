import { cn } from "../ui/cn";
import type { RunStatus } from "../../lib/api/types";

const color: Record<RunStatus, string> = {
  running: "bg-info",
  completed: "bg-success",
  errored: "bg-danger",
  unknown: "bg-faint",
};

/** Status dot; running dot emits a calm expanding ring. */
export function RunStatusDot(props: { status: RunStatus; size?: number; class?: string }) {
  const size = props.size ?? 8;
  return (
    <span class={cn("relative inline-flex shrink-0", props.class)} style={{ width: `${size}px`, height: `${size}px` }}>
      <span class={cn("absolute inset-0 rounded-full", color[props.status])} />
      {props.status === "running" && (
        <span
          class={cn("absolute inset-0 rounded-full text-info")}
          style={{ animation: "looper-ring 1.8s ease-out infinite" }}
        />
      )}
    </span>
  );
}
