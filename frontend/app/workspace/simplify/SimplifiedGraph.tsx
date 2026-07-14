"use client";

import { useEffect, useMemo } from "react";
import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  useStore,
  type Edge,
  type NodeTypes,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Crosshair, X } from "lucide-react";

import { CodeEntityNode } from "./CodeEntityNode";
import { GroupNode } from "./GroupNode";
import { GraphActionsContext } from "./GraphActions";
import { detailLevel, entityColor } from "./semantic";
import type { SynapseEdge, SynapseNode } from "./types";
import { useFocalGraph } from "./useFocalGraph";

export interface SimplifiedGraphProps {
  initialNodes: SynapseNode[];
  initialEdges: SynapseEdge[];
  /** Container ids collapsed on first render. */
  initialCollapsed?: string[];
}

// nodeTypes is module-level (a fresh object each render would remount all nodes).
const NODE_TYPES: NodeTypes = { codeEntity: CodeEntityNode, group: GroupNode };

const LEVEL_LABEL: Record<string, string> = {
  macro: "Macro · names only",
  mid: "Mid · structure",
  micro: "Micro · full detail",
};

function DetailBadge() {
  const level = useStore((s) => detailLevel(s.transform[2]));
  return (
    <span className="rounded-md border border-panel-border bg-black/60 px-2 py-1 font-mono text-[10px] text-neutral-400">
      {LEVEL_LABEL[level]}
    </span>
  );
}

function GraphInner({ initialNodes, initialEdges, initialCollapsed }: SimplifiedGraphProps) {
  const { nodes, edges, focalId, collapsedCount, focusNode, clearFocus, toggleCollapse } = useFocalGraph(
    initialNodes,
    initialEdges,
    initialCollapsed,
  );

  // Stable context value: callbacks are useCallback-stable, so nodes never
  // re-render merely because they read graph actions.
  const actions = useMemo(() => ({ focusNode, toggleCollapse }), [focusNode, toggleCollapse]);

  // Style edges once per visible-edge change (reroute = dashed indigo).
  const styledEdges = useMemo<Edge[]>(
    () =>
      edges.map((e) => {
        const rerouted = !!e.data?.rerouted;
        return {
          ...e,
          type: "smoothstep",
          animated: rerouted,
          markerEnd: { type: MarkerType.ArrowClosed, width: 15, height: 15, color: rerouted ? "#6366f1" : "#52525b" },
          style: rerouted
            ? { stroke: "#6366f1", strokeWidth: 1.5, strokeDasharray: "5 4" }
            : { stroke: "#52525b", strokeWidth: 1.5 },
        };
      }),
    [edges],
  );

  const focalName = useMemo(() => {
    if (!focalId) return null;
    const n = nodes.find((x) => x.id === focalId);
    return (n?.data.name as string) ?? focalId;
  }, [focalId, nodes]);

  // Re-frame the viewport when the focus lock or collapse state changes.
  const rf = useReactFlow();
  useEffect(() => {
    const t = setTimeout(() => rf.fitView({ padding: 0.25, duration: 450, maxZoom: 1.4 }), 60);
    return () => clearTimeout(t);
  }, [focalId, collapsedCount, rf]);

  return (
    <GraphActionsContext.Provider value={actions}>
      <div className="relative h-full w-full">
        {/* Toolbar */}
        <div className="pointer-events-none absolute inset-x-0 top-0 z-10 flex items-center justify-between gap-2 p-3">
          <div className="pointer-events-auto flex items-center gap-2">
            <span className="flex items-center gap-1.5 rounded-md border border-panel-border bg-black/60 px-2.5 py-1 font-mono text-[11px] font-semibold text-neutral-200">
              <span className="inline-block h-2 w-2 rounded-full bg-neon shadow-[0_0_10px_2px_var(--color-neon)]" />
              graph · simplified
            </span>
            <DetailBadge />
            {collapsedCount > 0 && (
              <span className="rounded-md border border-panel-border bg-black/60 px-2 py-1 font-mono text-[10px] text-neutral-400">
                {collapsedCount} collapsed
              </span>
            )}
          </div>

          {focalId && (
            <button
              onClick={clearFocus}
              className="pointer-events-auto flex items-center gap-1.5 rounded-md border border-accent/50 bg-accent/15 px-2.5 py-1 font-mono text-[11px] text-accent transition-colors hover:bg-accent/25"
            >
              <Crosshair size={12} />
              focus: {focalName}
              <X size={12} className="opacity-70" />
            </button>
          )}
        </div>

        <ReactFlow
          nodes={nodes}
          edges={styledEdges}
          nodeTypes={NODE_TYPES}
          colorMode="dark"
          fitView
          minZoom={0.2}
          maxZoom={2.4}
          nodesDraggable={false}
          onlyRenderVisibleElements
          proOptions={{ hideAttribution: true }}
        >
          <Background variant={BackgroundVariant.Dots} gap={26} size={1} color="#26262b" />
          <Controls showInteractive={false} className="!border-panel-border !bg-panel" />
          <MiniMap
            pannable
            zoomable
            maskColor="rgba(10,10,10,0.78)"
            nodeColor={(n) => entityColor(String((n.data as { entityType?: string }).entityType))}
            style={{ background: "#111113", border: "1px solid #26262b" }}
          />
        </ReactFlow>
      </div>
    </GraphActionsContext.Provider>
  );
}

/** Self-contained, drop-in simplified code-graph (wraps its own provider). */
export default function SimplifiedGraph(props: SimplifiedGraphProps) {
  return (
    <ReactFlowProvider>
      <GraphInner {...props} />
    </ReactFlowProvider>
  );
}
