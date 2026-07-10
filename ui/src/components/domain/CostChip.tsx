import { cn } from "../ui/cn";
import { usd } from "../../lib/format";
import { Tooltip } from "../ui/Tooltip";

/** Monospace cost figure; "~" marks an estimated (table-priced) value. */
export function CostChip(props: {
  value: number;
  estimated?: boolean;
  class?: string;
  tone?: "success" | "muted";
}) {
  const text = usd(props.value, props.estimated);
  const body = (
    <span
      class={cn(
        "font-mono text-[12px] tabular-nums",
        props.tone === "muted" ? "text-muted" : "text-success",
        props.class,
      )}
    >
      {text}
    </span>
  );
  return props.estimated ? (
    <Tooltip content="estimated from pricing tables">{body}</Tooltip>
  ) : (
    body
  );
}
