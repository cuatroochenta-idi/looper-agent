import { createSignal, For, Show, type JSX } from "solid-js";
import { cn } from "../ui/cn";
import { TraceNode } from "./TraceNode";
import { JsonViewer } from "./JsonViewer";
import { CopyButton } from "../ui/CopyButton";
import { Badge } from "../ui/Badge";
import type { ToolCall } from "../../lib/api/types";
import { duration, shortId } from "../../lib/format";

type Tab = "arguments" | "result" | "both";

/** Tool call/result node with an arguments/result/both view switch. */
export function ToolCallNode(props: { call: ToolCall; children?: JSX.Element }) {
  const [tab, setTab] = createSignal<Tab>(props.call.result?.is_error ? "result" : "arguments");
  const errored = () => props.call.result?.is_error ?? false;
  const running = () => !props.call.result;

  return (
    <TraceNode
      kind={errored() ? "tool · error" : "tool"}
      tone={errored() ? "danger" : "warning"}
      defaultOpen={errored()}
      live={running()}
      preview={
        <span class="font-mono text-[11.5px] text-text/80">{props.call.name}</span>
      }
      right={
        <span class="flex items-center gap-2">
          <Show when={props.call.spawned_run_ids.length > 0}>
            <Badge tone="accent" class="shrink-0">↳ {props.call.spawned_run_ids.length} spawned</Badge>
          </Show>
          <span class="font-mono text-[10.5px] text-faint">
            {running() ? "executing…" : `took ${duration(props.call.result!.at, props.call.result!.at)}`}
          </span>
        </span>
      }
    >
      <div class="mb-2 flex items-center gap-2">
        <div class="inline-flex rounded-[7px] border border-line bg-bg-raised p-0.5">
          <For each={["arguments", "result", "both"] as Tab[]}>
            {(t) => (
              <button
                onClick={() => setTab(t)}
                disabled={t !== "arguments" && running()}
                class={cn(
                  "rounded-[5px] px-2 py-0.5 text-[11px] font-medium transition-colors disabled:opacity-40",
                  tab() === t ? "bg-accent-soft text-accent" : "text-muted hover:text-text",
                )}
              >
                {t}
              </button>
            )}
          </For>
        </div>
        <CopyButton value={props.call.id} label={`call ${shortId(props.call.id)}`} class="ml-auto" />
      </div>

      <Show when={tab() === "arguments" || tab() === "both"}>
        <div class="mb-2">
          <div class="mb-1 text-[10.5px] uppercase tracking-wide text-faint">arguments</div>
          <JsonViewer raw={props.call.args_json} maxHeight="220px" />
        </div>
      </Show>

      <Show when={(tab() === "result" || tab() === "both") && props.call.result}>
        <div>
          <div class="mb-1 text-[10.5px] uppercase tracking-wide text-faint">result</div>
          <div
            class={cn(
              "overflow-auto rounded-[8px] border px-3 py-2 font-mono text-[11.5px] leading-[1.55] whitespace-pre-wrap",
              errored() ? "border-danger/30 bg-danger-soft text-danger" : "border-line bg-sunken text-muted",
            )}
            style={{ "max-height": "220px" }}
          >
            {props.call.result!.content || "(empty result)"}
          </div>
        </div>
      </Show>

      <Show when={props.children}>
        <div class="mt-2.5">{props.children}</div>
      </Show>
    </TraceNode>
  );
}
