import { createSignal } from "solid-js";
import { cn } from "./cn";

/** Id pill with a copy affordance: ⧉ → ✓ on success. */
export function CopyButton(props: { value: string; label?: string; class?: string }) {
  const [copied, setCopied] = createSignal(false);

  const copy = async (e: MouseEvent) => {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(props.value);
    } catch {
      /* clipboard blocked — still flash for feedback */
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 1100);
  };

  return (
    <button
      type="button"
      onClick={copy}
      title={`Copy ${props.value}`}
      class={cn(
        "group inline-flex items-center gap-1 rounded-[6px] border border-line bg-sunken px-1.5 py-0.5 " +
          "font-mono text-[11px] text-muted transition-colors hover:border-accent-line hover:text-text",
        props.class,
      )}
    >
      <span>{props.label ?? props.value}</span>
      <span class={cn("text-[11px]", copied() ? "text-success" : "text-faint group-hover:text-accent")}>
        {copied() ? "✓" : "⧉"}
      </span>
    </button>
  );
}
