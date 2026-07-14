"use client";

import { useEffect, useRef } from "react";
import { gsap } from "gsap";

interface ExecutionPanelProps {
  executionFlow: string[];
  highlightedFiles: string[];
  selectedLabel: string | null;
}

// ExecutionPanel is the sidebar "terminal": the line-by-line execution flow and
// the active file targets returned by the query engine. It fills its container
// (height is controlled by the surrounding ExecutionDock, which is drag-resizable)
// and scrolls internally. GSAP staggers the trace lines in for a high-fidelity reveal.
export default function ExecutionPanel({
  executionFlow,
  highlightedFiles,
  selectedLabel,
}: ExecutionPanelProps) {
  const linesRef = useRef<HTMLDivElement>(null);
  const hasContent = executionFlow.length > 0 || highlightedFiles.length > 0;

  useEffect(() => {
    if (!linesRef.current) return;
    const items = linesRef.current.querySelectorAll(".trace-line");
    if (items.length === 0) return;
    gsap.fromTo(
      items,
      { opacity: 0, x: -12 },
      { opacity: 1, x: 0, duration: 0.3, stagger: 0.05, ease: "power2.out" },
    );
  }, [executionFlow]);

  return (
    <div className="flex h-full min-h-0 flex-col bg-neutral-950/60">
      <div className="flex shrink-0 items-center justify-between px-4 pb-2 pt-2.5">
        <span className="font-mono text-[11px] uppercase tracking-widest text-neutral-500">
          execution trace
        </span>
        {selectedLabel && (
          <span className="truncate pl-2 font-mono text-[10px] text-accent">selected: {selectedLabel}</span>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-3">
        {!hasContent ? (
          <p className="font-mono text-[11px] text-neutral-600">
            ❯ awaiting query — run a question to trace the execution path.
          </p>
        ) : (
          <>
            <div ref={linesRef} className="space-y-1">
              {executionFlow.map((step, i) => (
                <div
                  key={i}
                  className="trace-line flex items-start gap-2 font-mono text-[11px] text-neutral-300"
                >
                  <span className="select-none text-emerald-400">❯</span>
                  <span className="text-neutral-600">{String(i + 1).padStart(2, "0")}</span>
                  <span className="min-w-0 break-words">{step}</span>
                </div>
              ))}
            </div>

            {highlightedFiles.length > 0 && (
              <div className="mt-3 border-t border-panel-border pt-2">
                <span className="font-mono text-[10px] uppercase tracking-widest text-neutral-600">
                  targets
                </span>
                <div className="mt-1.5 flex flex-wrap gap-1.5">
                  {highlightedFiles.map((f) => (
                    <span
                      key={f}
                      className="break-all rounded border border-panel-border bg-neutral-900 px-1.5 py-0.5 font-mono text-[10px] text-cyan-300"
                    >
                      {f}
                    </span>
                  ))}
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
