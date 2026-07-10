import { cn } from "../ui/cn";
import { splitModel } from "../../lib/format";

/** provider/model rendered as a compact two-tone chip. */
export function ModelChip(props: { model: string; fallback?: boolean; class?: string }) {
  const parts = () => splitModel(props.model);
  return (
    <span
      class={cn(
        "inline-flex items-center rounded-[6px] border border-line bg-sunken px-1.5 py-0.5 font-mono text-[11px] leading-none",
        props.class,
      )}
      title={props.model}
    >
      <span class="text-faint">{parts().provider}</span>
      {parts().provider && <span class="text-faint/50">/</span>}
      <span class="text-muted">{parts().model}</span>
      {props.fallback && <span class="ml-1 text-warning" title="served by a fallback key/provider">↪</span>}
    </span>
  );
}
