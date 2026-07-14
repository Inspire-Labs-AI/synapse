"use client";

import { createContext, useContext } from "react";

// Stable action callbacks are passed to custom nodes via context (NOT via node
// `data`). This keeps node data pure so React Flow's node memoization holds, and
// the context value never changes identity, so consuming a node action never
// causes an extra re-render.
export interface GraphActions {
  focusNode: (id: string) => void;
  toggleCollapse: (id: string) => void;
}

const noop = () => {};

export const GraphActionsContext = createContext<GraphActions>({
  focusNode: noop,
  toggleCollapse: noop,
});

export function useGraphActions(): GraphActions {
  return useContext(GraphActionsContext);
}
