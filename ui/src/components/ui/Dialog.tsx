import { Show, type JSX, onCleanup, onMount } from "solid-js";
import { Portal } from "solid-js/web";
import { cn } from "./cn";

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title?: JSX.Element;
  children: JSX.Element;
  class?: string;
}

export function Dialog(props: DialogProps) {
  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && props.open) props.onClose();
    };
    window.addEventListener("keydown", onKey);
    onCleanup(() => window.removeEventListener("keydown", onKey));
  });

  return (
    <Show when={props.open}>
      <Portal>
        <div class="fixed inset-0 z-[100] flex items-center justify-center p-4">
          <div
            class="absolute inset-0 bg-black/55 backdrop-blur-[2px] fade-up"
            onClick={props.onClose}
            aria-hidden="true"
          />
          <div
            role="dialog"
            aria-modal="true"
            class={cn(
              "relative w-full max-w-lg rounded-[14px] border border-line-strong bg-card p-5 shadow-[var(--shadow-pop)] fade-up",
              props.class,
            )}
          >
            <Show when={props.title}>
              <div class="mb-3 flex items-center justify-between">
                <h2 class="text-[14px] font-semibold text-text">{props.title}</h2>
                <button
                  onClick={props.onClose}
                  class="text-[16px] leading-none text-faint hover:text-text"
                  aria-label="Close dialog"
                >
                  ×
                </button>
              </div>
            </Show>
            {props.children}
          </div>
        </div>
      </Portal>
    </Show>
  );
}
