import type { SynapseNode } from "./types";

// Uniform, FIXED node footprints. Because every leaf is the same size and its
// content is distributed with flex (header top / detail middle / meta bottom),
// the layout can never overlap and never goes sparse — it stays clean at every
// zoom level. Containers are sized to exactly fit their visible children.
export const LEAF_W = 260;
export const LEAF_H = 128;
export const HEADER_H = 46;
export const PAD = 20;
export const GAP_Y = 20;
export const GAP_X = 96;
export const COLLAPSED_W = 260;
export const COLLAPSED_H = 46;

interface Size {
  w: number;
  h: number;
}

export interface LayoutResult {
  /** Only the currently-visible nodes, positioned + sized. */
  nodes: SynapseNode[];
  /** node id → the visible boundary its edges should route to (self if visible). */
  reroute: Map<string, string>;
}

/**
 * layoutGraph is collapse-aware: it lays out only the visible nodes for the given
 * collapsed set, sizing collapsed containers to their compact footprint and
 * excluding their hidden children. Re-running it on collapse therefore reflows
 * everything tightly (no leftover gaps). Pure + deterministic → memoize on
 * (nodes, collapsed).
 */
export function layoutGraph(nodes: SynapseNode[], collapsed: ReadonlySet<string>): LayoutResult {
  const childrenOf = new Map<string, string[]>();
  const parentOf = new Map<string, string | undefined>();
  for (const n of nodes) {
    parentOf.set(n.id, n.parentId);
    if (n.parentId) {
      const arr = childrenOf.get(n.parentId);
      if (arr) arr.push(n.id);
      else childrenOf.set(n.parentId, [n.id]);
    }
  }

  // Each node's outermost collapsed ancestor (the visible boundary), or itself.
  const reroute = new Map<string, string>();
  for (const n of nodes) {
    let target = n.id;
    let cur = parentOf.get(n.id);
    while (cur) {
      if (collapsed.has(cur)) target = cur;
      cur = parentOf.get(cur);
    }
    reroute.set(n.id, target);
  }

  const size = new Map<string, Size>();
  const relPos = new Map<string, { x: number; y: number }>();

  const measure = (id: string): Size => {
    const cached = size.get(id);
    if (cached) return cached;

    const kids = childrenOf.get(id);
    const hasKids = !!kids && kids.length > 0;

    if (collapsed.has(id) && hasKids) {
      const s = { w: COLLAPSED_W, h: COLLAPSED_H };
      size.set(id, s);
      return s;
    }
    if (!hasKids) {
      const s = { w: LEAF_W, h: LEAF_H };
      size.set(id, s);
      return s;
    }

    let y = HEADER_H + PAD;
    let maxW = LEAF_W;
    for (const kid of kids!) {
      const ks = measure(kid);
      relPos.set(kid, { x: PAD, y });
      y += ks.h + GAP_Y;
      maxW = Math.max(maxW, ks.w);
    }
    const s = { w: maxW + PAD * 2, h: y - GAP_Y + PAD };
    size.set(id, s);
    return s;
  };

  const tops = nodes.filter((n) => !n.parentId).map((n) => n.id);
  for (const id of tops) measure(id);

  const absPos = new Map<string, { x: number; y: number }>();
  let cursorX = 0;
  for (const id of tops) {
    absPos.set(id, { x: cursorX, y: 0 });
    cursorX += measure(id).w + GAP_X;
  }

  const out: SynapseNode[] = [];
  for (const n of nodes) {
    if (reroute.get(n.id) !== n.id) continue; // hidden inside a collapsed ancestor
    const kidCount = childrenOf.get(n.id)?.length ?? 0;
    const s = size.get(n.id) ?? { w: LEAF_W, h: LEAF_H };
    const position = n.parentId ? (relPos.get(n.id) ?? { x: 0, y: 0 }) : (absPos.get(n.id) ?? { x: 0, y: 0 });

    if (kidCount > 0) {
      out.push({
        ...n,
        position,
        style: { ...n.style, width: s.w, height: s.h },
        data: {
          ...n.data,
          collapsed: collapsed.has(n.id),
          childCount: kidCount,
          expandable: n.type === "codeEntity" ? true : n.data.expandable,
        },
      } as SynapseNode);
    } else {
      out.push({
        ...n,
        position,
        style: { ...n.style, width: LEAF_W, height: LEAF_H },
      } as SynapseNode);
    }
  }

  return { nodes: out, reroute };
}
