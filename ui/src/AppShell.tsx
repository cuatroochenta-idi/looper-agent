import { createSignal, For, Show, type JSX } from "solid-js";
import { A, useLocation } from "@solidjs/router";
import { cn } from "./components/ui/cn";
import { TimeRangePicker } from "./components/domain/TimeRangePicker";
import { ThemeToggle } from "./components/domain/ThemeToggle";
import { sseHub } from "./lib/state/sseHub";
import { IS_MOCK } from "./lib/api";

const NAV = [
  { href: "/", label: "Dashboard", match: (p: string) => p === "/" },
  { href: "/chats", label: "Chats", match: (p: string) => p.startsWith("/chats") },
  { href: "/runs", label: "Traces", match: (p: string) => p.startsWith("/runs") },
];

export function AppShell(props: { children: JSX.Element }) {
  const loc = useLocation();
  const [live, setLive] = createSignal(false);
  sseHub.onConnection(setLive);

  return (
    <div class="flex min-h-screen flex-col bg-bg">
      <header class="sticky top-0 z-40 border-b border-line bg-bg/80 backdrop-blur-md">
        <div class="mx-auto flex h-14 max-w-[1440px] items-center gap-4 px-5">
          {/* brand */}
          <A href="/" class="flex items-center gap-2 no-underline">
            <span class="relative flex h-2.5 w-2.5">
              <span class="absolute inset-0 rounded-full bg-accent" />
              <span class="pulse absolute inset-0 rounded-full bg-accent shadow-[0_0_10px_2px_var(--accent)]" />
            </span>
            <span class="text-[15px] font-semibold tracking-tight text-text">
              Looper<span class="text-accent">Agent</span>
            </span>
            <Show when={IS_MOCK}>
              <span class="rounded-[5px] border border-warning/30 bg-warning-soft px-1.5 py-0.5 text-[9.5px] font-medium uppercase tracking-wide text-warning">
                mock
              </span>
            </Show>
          </A>

          {/* nav pills */}
          <nav class="ml-2 flex items-center gap-0.5 rounded-[10px] border border-line bg-bg-raised p-0.5">
            <For each={NAV}>
              {(n) => {
                const active = () => n.match(loc.pathname);
                return (
                  <A
                    href={n.href}
                    class={cn(
                      "rounded-[8px] px-3 py-1.5 text-[12.5px] font-medium no-underline transition-colors",
                      active()
                        ? "bg-accent-soft text-accent shadow-[inset_0_0_0_1px_var(--accent-line)]"
                        : "text-muted hover:text-text hover:bg-input/60",
                    )}
                  >
                    {n.label}
                  </A>
                );
              }}
            </For>
          </nav>

          <div class="ml-auto flex items-center gap-2.5">
            <span
              class="flex items-center gap-1.5 text-[11px] text-faint"
              title={live() ? "live event stream connected" : "reconnecting to event stream"}
            >
              <span
                class={cn(
                  "h-1.5 w-1.5 rounded-full",
                  live() ? "bg-success pulse" : "bg-faint",
                )}
              />
              {live() ? "live" : "offline"}
            </span>
            <TimeRangePicker />
            <ThemeToggle />
          </div>
        </div>
      </header>

      <main class="mx-auto w-full max-w-[1440px] flex-1 px-5 py-5">{props.children}</main>
    </div>
  );
}
