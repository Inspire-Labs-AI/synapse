import type { Edge, Node } from "@xyflow/react";

// ---------------------------------------------------------------------------
// Graph data schema
//
// Two node kinds share one React Flow graph:
//   - "group"      structural containers (directories / modules) that nest
//                  children and can collapse.
//   - "codeEntity" leaf code (file / function / class / …). A code entity may
//                  ALSO own a subgraph (expandable), in which case it renders as
//                  a container too.
// ---------------------------------------------------------------------------

export type EntityType =
  | "directory"
  | "module"
  | "file"
  | "class"
  | "interface"
  | "function"
  | "method"
  | "endpoint";

/** Discrete level-of-detail derived from the viewport zoom (semantic zooming). */
export type DetailLevel = "macro" | "mid" | "micro";

/** Per-node egocentric role relative to the current focal node. */
export type FocalRole = "focal" | "caller" | "dependency";

export interface CodeEntityData extends Record<string, unknown> {
  name: string;
  entityType: EntityType;
  /** Package / language label shown as a badge in mid + micro detail. */
  package?: string;
  language?: string;
  /** Lines of code — a structural metric surfaced in mid view. */
  loc?: number;
  /** One-line AI / structural summary shown in micro view. */
  summary?: string;
  /** Short source excerpt shown in micro view. */
  snippet?: string;

  // --- runtime flags, stamped by the visible-graph builder (never authored) ---
  /** This node owns the subgraph the user can expand/collapse. */
  expandable?: boolean;
  collapsed?: boolean;
  childCount?: number;
  expandedWidth?: number;
  expandedHeight?: number;
  /** Egocentric role when a focal lock is active. */
  role?: FocalRole;
}

export interface GroupData extends Record<string, unknown> {
  name: string;
  entityType: "directory" | "module";
  childCount: number;
  collapsed?: boolean;
  /** Footprint to restore when expanded (children live inside it). */
  expandedWidth: number;
  expandedHeight: number;
  role?: FocalRole;
}

export type EntityFlowNode = Node<CodeEntityData, "codeEntity">;
export type GroupFlowNode = Node<GroupData, "group">;
export type SynapseNode = EntityFlowNode | GroupFlowNode;

export interface SynapseEdgeData extends Record<string, unknown> {
  /** True when collapse re-routed this edge to a group boundary. */
  rerouted?: boolean;
}
export type SynapseEdge = Edge<SynapseEdgeData>;

export function isGroup(n: SynapseNode): n is GroupFlowNode {
  return n.type === "group";
}
