"use client";

import { useEffect, useState } from "react";

import type { AxonStep } from "../lib/api";

// How long auto-play lingers on each step before flying to the next.
const STEP_MS = 7000;

// Role → colour / label / one-line "why you're here" hint. Keeps the tour legible
// for someone who has never seen the codebase.
const ROLE_COLOR: Record<string, string> = {
  foundation: "#34d399",
  core: "#6366f1",
  entry: "#22d3ee",
  consumer: "#fbbf24",
  docs: "#a855f7",
};
const ROLE_LABEL: Record<string, string> = {
  foundation: "foundation",
  core: "core module",
  entry: "entry point",
  consumer: "consumer",
  docs: "docs",
};
const ROLE_HINT: Record<string, string> = {
  foundation: "The rest of the code builds on this — a good first read.",
  core: "A core module wired into the middle of the dependency graph.",
  entry: "Where requests / execution enter the system.",
  consumer: "High-level glue that composes the lower-level pieces.",
  docs: "Project documentation — read for context before the code.",
};

interface TourOverlayProps {
  steps: AxonStep[];
  index: number;
  intro: string;
  onPrev: () => void;
  onNext: () => void;
  onJump: (i: number) => void;
  onExit: () => void;
  onFocusFile: (file: string) => void;
}

// TourOverlay — the "Axon Pathway" guided walkthrough. A terminal-styled HUD
// docked to the bottom of the canvas: a clickable step timeline, the current
// file's role + summary + key symbols, a reads/used-by dependency mini-map, and
// arrow-key navigation.
export default function TourOverlay({
  steps,
  index,
  intro,
  onPrev,
  onNext,
  onJump,
  onExit,
  onFocusFile,
}: TourOverlayProps) {
  const [playing, setPlaying] = useState(false);

  const step = steps[index];
  const atStart = index === 0;
  const atEnd = index === steps.length - 1;

  // Auto-play: fly to the next step on a timer. Manual navigation resets it
  // (the effect re-runs on `index`); reaching the end stops playback.
  useEffect(() => {
    if (!playing) return;
    if (atEnd) {
      setPlaying(false);
      return;
    }
    const t = setTimeout(onNext, STEP_MS);
    return () => clearTimeout(t);
  }, [playing, index, atEnd, onNext]);

  // Keyboard: ← / → move, Space toggles auto-play, Esc exits.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "ArrowRight") {
        e.preventDefault();
        setPlaying(false);
        onNext();
      } else if (e.key === "ArrowLeft") {
        e.preventDefault();
        setPlaying(false);
        onPrev();
      } else if (e.key === " ") {
        e.preventDefault();
        setPlaying((p) => !p);
      } else if (e.key === "Escape") {
        e.preventDefault();
        onExit();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onNext, onPrev, onExit]);

  if (!step) return null;

  const color = ROLE_COLOR[step.role] ?? "#94a3b8";
  const fileName = step.file.split("/").pop();

  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-0 z-20 flex justify-center px-3 pb-3 sm:px-4 sm:pb-4">
      <div className="axon-rise pointer-events-auto flex w-full max-w-3xl flex-col overflow-hidden rounded-xl border border-panel-border bg-neutral-950/95 shadow-2xl ring-1 ring-black/40 backdrop-blur-xl">
        {/* Progress strip — auto-play countdown, else overall completion */}
        <div className="h-0.5 w-full bg-white/[0.05]">
          {playing && !atEnd ? (
            <div
              key={index}
              className="h-full"
              style={{ backgroundColor: color, animation: `axon-progress ${STEP_MS}ms linear forwards` }}
            />
          ) : (
            <div
              className="h-full bg-accent/70 transition-[width] duration-500"
              style={{ width: `${((index + 1) / steps.length) * 100}%` }}
            />
          )}
        </div>

        {/* Terminal header + clickable step timeline */}
        <div className="flex items-center gap-2 border-b border-panel-border bg-black/50 px-3 py-2">
          <span className="hidden items-center gap-1.5 sm:flex">
            <span className="h-2 w-2 rounded-full bg-red-500/60" />
            <span className="h-2 w-2 rounded-full bg-amber-500/60" />
            <span className="h-2 w-2 rounded-full bg-emerald-500/60" />
          </span>
          <span className="font-mono text-[11px] text-neutral-500">axon · guided tour</span>
          <span className="font-mono text-[11px] text-accent">
            {String(index + 1).padStart(2, "0")}
            <span className="text-neutral-600">/{String(steps.length).padStart(2, "0")}</span>
          </span>
          <div className="ml-1 flex flex-1 items-center gap-1 overflow-x-auto py-1">
            {steps.map((s, i) => (
              <button
                key={s.order}
                onClick={() => onJump(i)}
                title={`${i + 1}. ${s.label}`}
                aria-label={`Go to step ${i + 1}: ${s.label}`}
                className="h-1.5 shrink-0 rounded-full transition-all hover:opacity-80"
                style={{
                  width: i === index ? 22 : 8,
                  backgroundColor:
                    i === index ? color : i < index ? "#52525b" : "#27272a",
                }}
              />
            ))}
          </div>
          <button
            onClick={onExit}
            className="ml-1 shrink-0 rounded px-1.5 py-0.5 font-mono text-[10px] text-neutral-500 transition-colors hover:bg-white/5 hover:text-neutral-200"
            title="Exit tour (Esc)"
          >
            esc ✕
          </button>
        </div>

        {/* Body */}
        <div className="max-h-[46vh] overflow-y-auto px-4 py-3.5">
          {index === 0 && intro && (
            <p className="mb-3 rounded-lg border border-accent/20 bg-accent/[0.06] px-3 py-2 text-[12.5px] leading-relaxed text-neutral-200">
              <span className="mr-1.5 font-mono text-[10px] uppercase tracking-wider text-accent">intro</span>
              {intro}
            </p>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <span
              className="shrink-0 rounded px-1.5 py-0.5 text-[9px] font-bold uppercase tracking-wider text-neutral-950"
              style={{ backgroundColor: color }}
            >
              {ROLE_LABEL[step.role] ?? step.role}
            </span>
            <span className="min-w-0 break-all font-mono text-[15px] font-semibold text-neutral-50">{fileName}</span>
            <button
              onClick={() => onFocusFile(step.file)}
              className="ml-auto flex shrink-0 items-center gap-1 rounded-md border border-panel-border px-2 py-1 font-mono text-[10px] text-neutral-400 transition-colors hover:border-cyan-400/50 hover:text-cyan-300"
              title="Recenter the canvas on this file"
            >
              ⊙ focus
            </button>
          </div>
          <div className="mt-0.5 break-all font-mono text-[10px] text-neutral-600">{step.file}</div>
          <p className="mt-1.5 text-[10px] italic leading-snug text-neutral-500">{ROLE_HINT[step.role] ?? ""}</p>

          <p className="mt-2.5 text-[13.5px] leading-relaxed text-neutral-200">{step.summary}</p>

          {step.symbols.length > 0 && (
            <div className="mt-3 flex flex-wrap items-center gap-1.5">
              <span className="font-mono text-[9px] uppercase tracking-wider text-neutral-600">symbols</span>
              {step.symbols.map((s) => (
                <code key={s} className="rounded bg-indigo-500/10 px-1.5 py-0.5 font-mono text-[10.5px] text-indigo-300">
                  {s}
                </code>
              ))}
            </div>
          )}

          <div className="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-2">
            <DepBox label="reads" arrow="→" files={step.imports} empty="nothing internal" color="#22d3ee" />
            <DepBox label="used by" arrow="←" files={step.imported_by} empty="no internal files" color="#fbbf24" />
          </div>
        </div>

        {/* Footer navigation */}
        <div className="flex items-center gap-2 border-t border-panel-border bg-black/50 px-3 py-2">
          <button
            onClick={() => setPlaying((p) => !p)}
            disabled={atEnd}
            className={
              "rounded-md border px-2.5 py-1 font-mono text-[11px] transition-colors disabled:cursor-not-allowed disabled:opacity-30 " +
              (playing
                ? "border-accent/60 bg-accent/10 text-accent"
                : "border-panel-border bg-neutral-900 text-neutral-300 hover:border-accent hover:text-accent")
            }
            title={playing ? "Pause auto-play (Space)" : "Auto-play the tour (Space)"}
          >
            {playing ? "⏸ pause" : "▶ play"}
          </button>
          <button onClick={onPrev} disabled={atStart} className={navBtn}>
            ‹ prev
          </button>
          <button onClick={onNext} disabled={atEnd} className={navBtn}>
            next ›
          </button>
          <span className="ml-auto hidden items-center gap-1.5 font-mono text-[10px] text-neutral-600 sm:flex">
            <Kbd>←</Kbd>
            <Kbd>→</Kbd>
            <Kbd>space</Kbd>
            {atEnd ? <span className="ml-2 text-emerald-400">✓ end of pathway</span> : "navigate"}
          </span>
          {atEnd && <span className="ml-auto font-mono text-[10px] text-emerald-400 sm:hidden">✓ end</span>}
        </div>
      </div>
    </div>
  );
}

const navBtn =
  "rounded-md border border-panel-border bg-neutral-900 px-2.5 py-1 font-mono text-[11px] text-neutral-300 transition-colors hover:border-accent hover:text-accent disabled:cursor-not-allowed disabled:opacity-30";

function Kbd({ children }: { children: React.ReactNode }) {
  return (
    <kbd className="rounded border border-panel-border bg-neutral-900 px-1 py-0.5 font-mono text-[9px] leading-none text-neutral-400">
      {children}
    </kbd>
  );
}

function DepBox({
  label,
  arrow,
  files,
  empty,
  color,
}: {
  label: string;
  arrow: string;
  files: string[];
  empty: string;
  color: string;
}) {
  return (
    <div className="min-w-0 rounded-lg border border-panel-border bg-neutral-900/40 px-2.5 py-1.5">
      <div className="flex items-center gap-1.5">
        <span className="font-mono text-[9px] uppercase tracking-wider" style={{ color }}>
          {arrow} {label}
        </span>
        <span className="font-mono text-[9px] text-neutral-600">{files.length}</span>
      </div>
      <div className="mt-1 flex flex-wrap gap-1">
        {files.length ? (
          files.slice(0, 6).map((f) => (
            <code key={f} className="rounded bg-black/40 px-1.5 py-0.5 font-mono text-[10px] text-neutral-400" title={f}>
              {f.split("/").pop()}
            </code>
          ))
        ) : (
          <span className="font-mono text-[10px] text-neutral-600">{empty}</span>
        )}
        {files.length > 6 && <span className="font-mono text-[10px] text-neutral-600">+{files.length - 6}</span>}
      </div>
    </div>
  );
}
