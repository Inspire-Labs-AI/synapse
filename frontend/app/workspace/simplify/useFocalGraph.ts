"use client";

import { useCallback, useMemo, useState } from "react";

import { LEAF_H, LEAF_W, layoutGraph } from "./graphLayout";
import type { SynapseEdge, SynapseNode } from "./types";

/** Re-route + de-duplicate edges across collapsed boundaries. */
function resolveEdges(edges: SynapseEdge[], reroute: Map<string, string>): SynapseEdge[] {
  const seen = new Set<string>();
  const out: SynapseEdge[] = [];
  for (const e of edges) {
    const s = reroute.get(e.source) ?? e.source;
    const t = reroute.get(e.target) ?? e.target;
    if (s === t) continue; // fully inside one collapsed boundary
    const key = `${s} ${t}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const rerouted = s !== e.source || t !== e.target;
    out.push({
      ...e,
      id: rerouted ? `reroute:${s}->${t}` : e.id,
      source: s,
      target: t,
      data: { ...e.data, rerouted },
    });
  }
  return out;
}

// Egocentric layout geometry.
const FOCAL_COL = 400;
const FOCAL_ROW = 156;

interface View {
  nodes: SynapseNode[];
  edges: SynapseEdge[];
}

/**
 * Egocentric focus: keep only the focal node, its direct callers (incoming),
 * direct dependencies (outgoing), and edges incident to it. Neighbors are
 * flattened (parentId stripped) and re-laid-out around the focal node.
 */
function applyFocal(nodes: SynapseNode[], edges: SynapseEdge[], focalId: string | null): View {
  if (!focalId) return { nodes, edges };
  const present = new Set(nodes.map((n) => n.id));
  if (!present.has(focalId)) return { nodes, edges }; // focal collapsed away — ignore

  const callers: string[] = [];
  const deps: string[] = [];
  const keepEdges: SynapseEdge[] = [];
  for (const e of edges) {
    if (e.target === focalId && e.source !== focalId) {
      callers.push(e.source);
      keepEdges.push(e);
    } else if (e.source === focalId && e.target !== focalId) {
      deps.push(e.target);
      keepEdges.push(e);
    }
  }

  const callerSet = new Set(callers);
  const depSet = new Set(deps);
  const keep = new Set<string>([focalId, ...callers, ...deps]);

  const pos = new Map<string, { x: number; y: number }>();
  pos.set(focalId, { x: 0, y: 0 });
  callers.forEach((c, i) => pos.set(c, { x: -FOCAL_COL, y: (i - (callers.length - 1) / 2) * FOCAL_ROW }));
  deps.forEach((d, i) => pos.set(d, { x: FOCAL_COL, y: (i - (deps.length - 1) / 2) * FOCAL_ROW }));

  const out: SynapseNode[] = [];
  for (const n of nodes) {
    if (!keep.has(n.id)) continue;
    const role = n.id === focalId ? "focal" : callerSet.has(n.id) ? "caller" : depSet.has(n.id) ? "dependency" : undefined;
    out.push({
      ...n,
      parentId: undefined,
      extent: undefined,
      position: pos.get(n.id) ?? n.position,
      // Flatten to uniform cards (never containers) in the isolated focal view.
      style: { ...n.style, width: LEAF_W, height: LEAF_H },
      data: { ...n.data, role, collapsed: false, expandable: false },
    } as SynapseNode);
  }
  return { nodes: out, edges: keepEdges };
}

export interface FocalGraph {
  nodes: SynapseNode[];
  edges: SynapseEdge[];
  focalId: string | null;
  collapsedCount: number;
  focusNode: (id: string) => void;
  clearFocus: () => void;
  toggleCollapse: (id: string) => void;
}

/**
 * useFocalGraph composes collapse-aware layout + egocentric focus over an
 * immutable raw graph. Each stage memoizes on its real inputs, so pan/zoom never
 * recomputes the visible set and a click recomputes only the affected stage.
 */
export function useFocalGraph(
  rawNodes: SynapseNode[],
  rawEdges: SynapseEdge[],
  initialCollapsed?: Iterable<string>,
): FocalGraph {
  const [focalId, setFocalId] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set(initialCollapsed ?? []));

  const laid = useMemo(() => layoutGraph(rawNodes, collapsed), [rawNodes, collapsed]);
  const edges = useMemo(() => resolveEdges(rawEdges, laid.reroute), [rawEdges, laid.reroute]);
  const view = useMemo(() => applyFocal(laid.nodes, edges, focalId), [laid.nodes, edges, focalId]);

  const focusNode = useCallback((id: string) => setFocalId(id), []);
  const clearFocus = useCallback(() => setFocalId(null), []);
  const toggleCollapse = useCallback((id: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  return {
    nodes: view.nodes,
    edges: view.edges,
    focalId,
    collapsedCount: collapsed.size,
    focusNode,
    clearFocus,
    toggleCollapse,
  };
}
