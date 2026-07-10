import { For, Show } from "solid-js";
import { RunCard } from "./RunCard";
import type { RunNode } from "../../lib/state/runTree";

/**
 * Recursive run forest. Children indent with a vertical connector rail and a
 * short horizontal elbow into each card — the tree structure the legacy panel
 * drew with ::before/::after, rebuilt as real nested nodes.
 */
export function RunTree(props: {
  nodes: RunNode[];
  selectedId?: string;
  onSelect: (id: string) => void;
}) {
  return (
    <div class="flex flex-col gap-1.5">
      <For each={props.nodes}>
        {(node) => (
          <div class="flex flex-col gap-1.5">
            <RunCard
              run={node.run}
              orphan={node.orphan}
              selected={props.selectedId === node.run.id}
              onClick={() => props.onSelect(node.run.id)}
            />
            <Show when={node.children.length > 0}>
              <div class="relative ml-3 border-l border-line pl-3">
                <RunTree
                  nodes={node.children}
                  selectedId={props.selectedId}
                  onSelect={props.onSelect}
                />
              </div>
            </Show>
          </div>
        )}
      </For>
    </div>
  );
}
