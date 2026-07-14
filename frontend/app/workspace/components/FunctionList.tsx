"use client";

import { useEffect, useRef, useState } from "react";
import { gsap } from "gsap";

import type { FunctionHit } from "../lib/api";

interface FunctionListProps {
  functions: FunctionHit[];
  onFocusFile?: (file: string) => void;
}

// Renders the symbol-level functions a query matched, each collapsed to a header
// row that expands to reveal the source code.
export default function FunctionList({ functions, onFocusFile }: FunctionListProps) {
  const rootRef = useRef<HTMLDivElement>(null);

  // Coordinated reveal: fade the evidence in when it appears (after the answer).
  useEffect(() => {
    if (rootRef.current) {
      gsap.fromTo(rootRef.current, { opacity: 0, y: 6 }, { opacity: 1, y: 0, duration: 0.4, ease: "power2.out" });
    }
  }, []);

  if (!functions || functions.length === 0) return null;
  return (
    <div ref={rootRef} className="mt-2.5 min-w-0 space-y-1">
      <div className="text-[10px] uppercase tracking-widest text-neutral-500">
        responsible functions
      </div>
      {functions.map((f, i) => (
        <FunctionItem key={`${f.file}:${f.symbol}:${i}`} fn={f} onFocusFile={onFocusFile} />
      ))}
    </div>
  );
}

function FunctionItem({
  fn,
  onFocusFile,
}: {
  fn: FunctionHit;
  onFocusFile?: (file: string) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="overflow-hidden rounded-md border border-panel-border bg-neutral-950/60">
      <div className="flex items-center gap-2 px-2.5 py-1.5">
        <button
          onClick={() => setOpen((o) => !o)}
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
        >
          <span className="select-none text-[9px] text-neutral-500">{open ? "▾" : "▸"}</span>
          <span className="truncate font-mono text-[12px] text-neutral-100">{fn.symbol}</span>
          <span className="shrink-0 text-[9px] uppercase tracking-wider text-neutral-600">
            {fn.chunk_type}
          </span>
        </button>
        <button
          onClick={() => onFocusFile?.(fn.file)}
          className="min-w-0 max-w-[50%] shrink truncate font-mono text-[10px] text-neutral-500 transition-colors hover:text-cyan-300"
          title={`Focus ${fn.file} on the canvas`}
          disabled={!onFocusFile}
        >
          {fn.file}:{fn.start_line}
        </button>
      </div>
      {open && (
        <pre className="max-h-64 overflow-auto border-t border-panel-border bg-black px-3 py-2 font-mono text-[11px] leading-relaxed text-neutral-200">
          <code>{fn.code}</code>
        </pre>
      )}
    </div>
  );
}
