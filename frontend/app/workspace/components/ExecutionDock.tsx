"use client";

import { useRef, useState } from "react";

import ExecutionPanel from "./ExecutionPanel";

interface ExecutionDockProps {
  executionFlow: string[];
  highlightedFiles: string[];
  selectedLabel: string | null;
}

const MIN_H = 96;
const MAX_H = 600;
const DEFAULT_H = 220;

// ExecutionDock holds the execution trace + targets panel and makes its height
// drag-resizable via a grab handle on its top edge. Dragging up enlarges it
// (the chat area above shrinks via flex); the size is clamped to a usable range.
export default function ExecutionDock(props: ExecutionDockProps) {
  const [height, setHeight] = useState(DEFAULT_H);
  const [dragging, setDragging] = useState(false);
  const drag = useRef<{ startY: number; startH: number } | null>(null);

  function onDown(e: React.PointerEvent) {
    e.preventDefault();
    drag.current = { startY: e.clientY, startH: height };
    setDragging(true);
    e.currentTarget.setPointerCapture(e.pointerId);
  }
  function onMove(e: React.PointerEvent) {
    if (!drag.current) return;
    const delta = drag.current.startY - e.clientY; // drag up => taller
    setHeight(Math.min(MAX_H, Math.max(MIN_H, drag.current.startH + delta)));
  }
  function onUp(e: React.PointerEvent) {
    drag.current = null;
    setDragging(false);
    try {
      e.currentTarget.releasePointerCapture(e.pointerId);
    } catch {
      /* pointer already released */
    }
  }

  return (
    <>
      <div
        onPointerDown={onDown}
        onPointerMove={onMove}
        onPointerUp={onUp}
        title="Drag to resize"
        className={
          "group flex shrink-0 cursor-row-resize touch-none select-none items-center justify-center border-t border-panel-border py-1 transition-colors hover:bg-neutral-800/50 " +
          (dragging ? "bg-neutral-800/60" : "")
        }
      >
        <span
          className={
            "h-1 w-10 rounded-full transition-colors " +
            (dragging ? "bg-accent" : "bg-neutral-700 group-hover:bg-neutral-500")
          }
        />
      </div>
      <div className="min-h-0 shrink-0" style={{ height }}>
        <ExecutionPanel {...props} />
      </div>
    </>
  );
}
