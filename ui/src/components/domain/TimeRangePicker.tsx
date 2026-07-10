import { createSignal, For, Show } from "solid-js";
import { cn } from "../ui/cn";
import { PRESETS, range, setCustom, setPreset, type RangePreset } from "../../lib/state/timeRange";
import { Input } from "../ui/Input";

/** 15m / 1h / 24h / all preset pills + a custom datetime pair. */
export function TimeRangePicker() {
  const [customOpen, setCustomOpen] = createSignal(false);
  const [from, setFrom] = createSignal("");
  const [to, setTo] = createSignal("");

  const active = (id: RangePreset) => range().preset === id;

  const applyCustom = () => {
    if (!from()) return;
    setCustom(new Date(from()).toISOString(), to() ? new Date(to()).toISOString() : undefined);
    setCustomOpen(false);
  };

  return (
    <div class="relative flex items-center gap-0.5 rounded-[10px] border border-line bg-bg-raised p-0.5">
      <For each={PRESETS}>
        {(p) => (
          <button
            onClick={() => setPreset(p.id)}
            class={cn(
              "h-7 rounded-[8px] px-2.5 text-[12px] font-medium transition-colors",
              active(p.id)
                ? "bg-accent-soft text-accent shadow-[inset_0_0_0_1px_var(--accent-line)]"
                : "text-muted hover:text-text hover:bg-input/60",
            )}
          >
            {p.label}
          </button>
        )}
      </For>
      <button
        onClick={() => setCustomOpen((v) => !v)}
        class={cn(
          "h-7 rounded-[8px] px-2.5 text-[12px] font-medium transition-colors",
          active("custom")
            ? "bg-accent-soft text-accent shadow-[inset_0_0_0_1px_var(--accent-line)]"
            : "text-muted hover:text-text hover:bg-input/60",
        )}
      >
        custom
      </button>

      <Show when={customOpen()}>
        <div class="absolute right-0 top-[calc(100%+6px)] z-50 w-72 rounded-[12px] border border-line-strong bg-card p-3 shadow-[var(--shadow-pop)] fade-up">
          <div class="mb-2 text-[11px] font-medium uppercase tracking-wide text-faint">custom range</div>
          <label class="mb-1 block text-[11px] text-muted">from</label>
          <Input type="datetime-local" value={from()} onInput={(e) => setFrom(e.currentTarget.value)} class="mb-2" />
          <label class="mb-1 block text-[11px] text-muted">to (optional)</label>
          <Input type="datetime-local" value={to()} onInput={(e) => setTo(e.currentTarget.value)} class="mb-3" />
          <div class="flex justify-end gap-2">
            <button
              class="h-7 rounded-[8px] px-2.5 text-[12px] text-muted hover:text-text"
              onClick={() => setCustomOpen(false)}
            >
              cancel
            </button>
            <button
              class="h-7 rounded-[8px] bg-accent px-3 text-[12px] font-medium text-[#0a0d12] hover:brightness-110 disabled:opacity-40"
              disabled={!from()}
              onClick={applyCustom}
            >
              apply
            </button>
          </div>
        </div>
      </Show>
    </div>
  );
}
