import type { DetailLevel } from "./types";

// Semantic-zoom thresholds. Kept as module constants so the node component and
// the toolbar agree on the exact bucket boundaries.
export const ZOOM_MACRO_MAX = 0.5; // below this => macro (compressed)
export const ZOOM_MID_MAX = 0.8; // [0.5, 0.8] => mid; above => micro (full detail)

/**
 * Map a continuous viewport zoom to a discrete detail bucket. Selecting THIS
 * (rather than the raw zoom) inside `useStore` means a node only re-renders when
 * it crosses a threshold — never on every wheel tick / animation frame.
 */
export function detailLevel(zoom: number): DetailLevel {
  if (zoom < ZOOM_MACRO_MAX) return "macro";
  if (zoom <= ZOOM_MID_MAX) return "mid";
  return "micro";
}

// Per-entity accent colors (dark-neon developer-tool palette).
export const ENTITY_COLOR: Record<string, string> = {
  directory: "#8b5cf6",
  module: "#6366f1",
  file: "#22d3ee",
  class: "#34d399",
  interface: "#2dd4bf",
  function: "#fbbf24",
  method: "#f59e0b",
  endpoint: "#f472b6",
};

export function entityColor(entityType: string): string {
  return ENTITY_COLOR[entityType] ?? "#94a3b8";
}
