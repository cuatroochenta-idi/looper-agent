import type { RunListItem } from "../api/types";

export interface RunNode {
  run: RunListItem;
  children: RunNode[];
  depth: number;
  /** subagent whose parent is not in the set — surfaced at top level. */
  orphan: boolean;
}

/**
 * Build a forest from a flat run list. Top-level nodes are `kind: "run"` plus
 * any orphaned subagents (parent id present but parent absent from the set).
 * Subagents attach under their parent; the parent never appears twice.
 */
export function buildForest(runs: RunListItem[]): RunNode[] {
  const byId = new Map<string, RunListItem>();
  for (const r of runs) byId.set(r.id, r);

  const childrenOf = new Map<string, RunListItem[]>();
  for (const r of runs) {
    if (r.parent_run_id && byId.has(r.parent_run_id)) {
      const list = childrenOf.get(r.parent_run_id) ?? [];
      list.push(r);
      childrenOf.set(r.parent_run_id, list);
    }
  }

  const make = (run: RunListItem, depth: number, orphan: boolean): RunNode => {
    const kids = (childrenOf.get(run.id) ?? [])
      .slice()
      .sort((a, b) => Date.parse(a.started_at) - Date.parse(b.started_at))
      .map((c) => make(c, depth + 1, false));
    return { run, children: kids, depth, orphan };
  };

  const roots: RunNode[] = [];
  for (const r of runs) {
    const isTop = r.kind === "run" || !r.parent_run_id;
    const isOrphan = r.kind === "subagent" && !!r.parent_run_id && !byId.has(r.parent_run_id);
    if (isTop || isOrphan) roots.push(make(r, 0, isOrphan));
  }
  roots.sort((a, b) => Date.parse(b.run.started_at) - Date.parse(a.run.started_at));
  return roots;
}

export interface SessionGroup {
  session_id: string;
  nodes: RunNode[];
  self_usd: number;
  running: boolean;
}

/** Group top-level forest nodes by session for the Traces sidebar. */
export function groupBySession(roots: RunNode[]): SessionGroup[] {
  const groups = new Map<string, SessionGroup>();
  for (const node of roots) {
    const sid = node.run.session_id || "unsessioned";
    const g = groups.get(sid) ?? { session_id: sid, nodes: [], self_usd: 0, running: false };
    g.nodes.push(node);
    g.self_usd += node.run.subtree_usd;
    if (node.run.status === "running") g.running = true;
    groups.set(sid, g);
  }
  const list = [...groups.values()];
  // Most recently active sessions first.
  const lastSeen = (g: SessionGroup) =>
    Math.max(...g.nodes.map((n) => Date.parse(n.run.last_seen_at)));
  list.sort((a, b) => lastSeen(b) - lastSeen(a));
  return list;
}
