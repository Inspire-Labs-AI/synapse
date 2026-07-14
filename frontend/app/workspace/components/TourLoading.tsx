"use client";

import { useEffect, useState } from "react";

// The Axon pathway is computed by an LLM pass over the topological sort — a few
// seconds of silence otherwise. This overlay makes that wait feel intentional:
// a neural pulse + cycling stage labels + an indeterminate scan bar.
const STAGES = [
  "Reading the dependency graph",
  "Computing the topological order",
  "Selecting entry points & foundations",
  "Composing your guided walkthrough",
];

export default function TourLoading() {
  const [stage, setStage] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setStage((v) => Math.min(v + 1, STAGES.length - 1)), 1700);
    return () => clearInterval(t);
  }, []);

  return (
    <div className="absolute inset-0 z-30 flex items-center justify-center bg-[#08080b]/72 backdrop-blur-md">
      <div className="axon-rise flex w-[19rem] flex-col items-center gap-5 rounded-2xl border border-white/[0.08] bg-black/60 px-8 py-9 shadow-2xl shadow-black/50 backdrop-blur-xl">
        {/* neural pulse */}
        <div className="relative flex h-14 w-14 items-center justify-center">
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-indigo-400/20" />
          <span className="absolute inline-flex h-9 w-9 animate-ping rounded-full bg-indigo-400/30 [animation-delay:180ms]" />
          <span className="relative inline-flex h-4 w-4 rounded-full bg-indigo-400 shadow-[0_0_18px_4px_rgba(129,140,248,0.6)]" />
        </div>

        <div className="flex flex-col items-center gap-1 text-center">
          <div className="text-[13.5px] font-semibold tracking-tight text-neutral-100">
            Charting the Axon pathway
          </div>
          <div className="h-4 font-mono text-[11px] text-indigo-300/90 transition-opacity">
            {STAGES[stage]}
            <span className="dot-pulse">…</span>
          </div>
        </div>

        {/* indeterminate scan bar */}
        <div className="relative h-[3px] w-full overflow-hidden rounded-full bg-white/[0.06]">
          <span className="axon-scan absolute inset-y-0 left-0 w-1/3 rounded-full bg-gradient-to-r from-transparent via-indigo-400 to-transparent" />
        </div>

        {/* stage ticks */}
        <div className="flex items-center gap-1.5">
          {STAGES.map((_, i) => (
            <span
              key={i}
              className={
                "h-1 rounded-full transition-all duration-500 " +
                (i <= stage ? "w-5 bg-indigo-400/90" : "w-2 bg-white/10")
              }
            />
          ))}
        </div>
      </div>
    </div>
  );
}
