"use client";

import { useEffect, useRef, useState } from "react";
import { gsap } from "gsap";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

import { streamDiscover, type BlueprintMode, type BlueprintResponse } from "../lib/api";

interface BlueprintPanelProps {
  repo: string | null; // active repo root_path to scope discovery to
  onResult: (bp: BlueprintResponse | null) => void;
}

const categoryColor: Record<string, string> = {
  green: "#34d399",
  yellow: "#fbbf24",
  red: "#f87171",
};

// Models occasionally wrap the whole answer in a ```markdown fence — strip it so
// ReactMarkdown renders the content instead of showing a raw code block.
function stripFence(s: string): string {
  return s
    .replace(/^\s*```(?:markdown|md)?[ \t]*\n?/i, "")
    .replace(/\n?```\s*$/i, "");
}

const MODES: { id: BlueprintMode; label: string; hint: string }[] = [
  { id: "validate", label: "Validate", hint: "Should you build it? Impact, reuse leverage & effort." },
  { id: "roadmap", label: "Roadmap", hint: "The build plan: what to reuse, extend & create — and where." },
];

export default function BlueprintPanel({ repo, onResult }: BlueprintPanelProps) {
  const [mode, setMode] = useState<BlueprintMode>("roadmap");
  const [resultMode, setResultMode] = useState<BlueprintMode>("roadmap");
  const [description, setDescription] = useState("");
  const [loading, setLoading] = useState(false);
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<BlueprintResponse | null>(null);
  const [analysis, setAnalysis] = useState("");
  const diffRef = useRef<HTMLDivElement>(null);

  // Stagger the diff rows in when a result lands.
  useEffect(() => {
    if (!result || !diffRef.current) return;
    const rows = diffRef.current.querySelectorAll(".diff-row");
    if (rows.length) {
      gsap.fromTo(
        rows,
        { opacity: 0, x: -10 },
        { opacity: 1, x: 0, duration: 0.3, stagger: 0.04, ease: "power2.out" },
      );
    }
  }, [result]);

  async function discover(e: React.FormEvent) {
    e.preventDefault();
    const desc = description.trim();
    if (!desc || loading) return;
    const submittedMode = mode;
    setLoading(true);
    setStreaming(true);
    setError(null);
    setAnalysis("");
    let acc = "";
    try {
      await streamDiscover(desc, repo, submittedMode, {
        onResult: (bp) => {
          setResult(bp);
          setResultMode(submittedMode);
          onResult(bp);
        },
        onToken: (delta) => {
          acc += delta;
          setAnalysis(acc);
        },
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "discovery failed");
      setResult(null);
      onResult(null);
    } finally {
      setLoading(false);
      setStreaming(false);
    }
  }

  function clear() {
    setResult(null);
    setError(null);
    setDescription("");
    setAnalysis("");
    onResult(null);
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-panel-border px-4 py-3">
        <span className="inline-block h-2 w-2 rounded-full bg-neon shadow-[0_0_10px_2px_var(--color-neon)]" />
        <span className="font-mono text-xs font-semibold tracking-tight text-neutral-200">
          capability discovery
        </span>
        <span className="text-[10px] uppercase tracking-widest text-neutral-600">blueprint</span>
      </div>

      <div className="flex-1 overflow-y-auto px-4 py-4">
        <form onSubmit={discover} className="space-y-2">
          {/* Validate / Roadmap mode toggle */}
          <div className="flex rounded-lg border border-panel-border bg-neutral-900 p-0.5">
            {MODES.map((m) => (
              <button
                key={m.id}
                type="button"
                onClick={() => setMode(m.id)}
                disabled={loading}
                className={
                  "flex-1 rounded-md px-3 py-1.5 text-[12px] font-medium transition-colors disabled:opacity-50 " +
                  (mode === m.id ? "bg-neon/15 text-neon" : "text-neutral-400 hover:text-neutral-200")
                }
              >
                {m.label}
              </button>
            ))}
          </div>
          <p className="px-0.5 text-[11px] leading-snug text-neutral-500">
            {MODES.find((m) => m.id === mode)?.hint}
          </p>

          <label className="block pt-1 text-[11px] uppercase tracking-widest text-neutral-500">
            Feature idea
          </label>
          <textarea
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Add a billing subscription module where users can subscribe to a plan and modify their category quota…"
            rows={3}
            disabled={loading}
            className="w-full resize-none rounded-lg border border-panel-border bg-neutral-900 px-3 py-2 text-[13px] text-neutral-100 outline-none placeholder:text-neutral-600 focus:border-neon disabled:opacity-50"
          />
          <div className="flex gap-2">
            <button
              type="submit"
              disabled={!description.trim() || loading}
              className="flex-1 rounded-lg bg-neon px-3 py-2 text-[13px] font-medium text-neutral-950 transition-colors hover:brightness-110 disabled:opacity-40"
            >
              {loading
                ? mode === "validate"
                  ? "Validating…"
                  : "Charting…"
                : mode === "validate"
                  ? "Validate feature"
                  : "Generate roadmap"}
            </button>
            {result && (
              <button
                type="button"
                onClick={clear}
                className="rounded-lg border border-panel-border px-3 py-2 text-[13px] text-neutral-400 hover:border-neutral-500"
              >
                Clear
              </button>
            )}
          </div>
        </form>

        {error && (
          <p className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-[12px] text-red-300">
            {error}. Is the backend running on :8080?
          </p>
        )}

        {result && (
          <div className="mt-4 space-y-4">
            {/* Reuse summary */}
            <div className="grid grid-cols-4 gap-2 text-center">
              {[
                { k: "Reuse", v: result.summary.green, c: "green" },
                { k: "Extend", v: result.summary.yellow, c: "yellow" },
                { k: "Build", v: result.summary.red, c: "red" },
                { k: "Score", v: `${Math.round(result.summary.reuse_score * 100)}%`, c: "neon" },
              ].map((s) => (
                <div key={s.k} className="rounded-lg border border-panel-border bg-neutral-900 py-2">
                  <div
                    className="font-mono text-lg font-semibold"
                    style={{ color: s.c === "neon" ? "#22d3ee" : categoryColor[s.c] }}
                  >
                    {s.v}
                  </div>
                  <div className="text-[9px] uppercase tracking-widest text-neutral-500">{s.k}</div>
                </div>
              ))}
            </div>

            {/* Streamed reuse briefing */}
            {(analysis || streaming) && (
              <div className="rounded-lg border border-panel-border bg-neutral-900/60 px-3 py-2.5">
                <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-widest text-neutral-500">
                  <span className="inline-block h-1.5 w-1.5 rounded-full bg-neon" />
                  {resultMode === "validate" ? "validation" : "blueprint"}
                </div>
                <div className="md text-[12.5px] text-neutral-300">
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>{stripFence(analysis)}</ReactMarkdown>
                  {streaming && (
                    <span className="ml-0.5 inline-block h-3.5 w-[2px] -translate-y-px animate-pulse bg-neon align-middle" />
                  )}
                </div>
              </div>
            )}

            {/* Capability matrix */}
            <div>
              <div className="mb-1.5 text-[10px] uppercase tracking-widest text-neutral-500">
                capability matrix
              </div>
              <div className="space-y-1">
                {result.matches.map((m) => (
                  <div
                    key={m.kind + m.name}
                    className="flex items-center gap-2 rounded-md border border-panel-border bg-neutral-900 px-2.5 py-1.5"
                  >
                    <span
                      className="h-2 w-2 shrink-0 rounded-full"
                      style={{ backgroundColor: categoryColor[m.category] }}
                    />
                    <span className="font-mono text-[12px] text-neutral-200">{m.name}</span>
                    <span className="text-[9px] uppercase tracking-wider text-neutral-600">{m.kind}</span>
                    <span className="ml-auto font-mono text-[10px] text-neutral-500">
                      {Math.round(m.confidence * 100)}%
                    </span>
                  </div>
                ))}
              </div>
            </div>

            {/* Diff summary — the file-level build plan (roadmap only) */}
            {resultMode === "roadmap" && (
            <div ref={diffRef}>
              <div className="mb-1.5 text-[10px] uppercase tracking-widest text-neutral-500">
                files to change
              </div>
              <div className="space-y-1">
                {result.diff_summary.length === 0 && (
                  <p className="text-[12px] text-neutral-600">
                    Everything required already exists — full reuse.
                  </p>
                )}
                {result.diff_summary.map((d, i) => (
                  <div
                    key={i}
                    className="diff-row flex items-start gap-2 rounded-md border-l-2 bg-neutral-900/60 px-2.5 py-1.5"
                    style={{ borderColor: categoryColor[d.category] }}
                  >
                    <span
                      className="mt-0.5 shrink-0 rounded px-1 py-0.5 font-mono text-[9px] font-bold uppercase text-neutral-950"
                      style={{ backgroundColor: categoryColor[d.category] }}
                    >
                      {d.change_type === "create" ? "+new" : "~edit"}
                    </span>
                    <div className="min-w-0">
                      <div className="truncate font-mono text-[11px] text-neutral-200">{d.file}</div>
                      <div className="text-[10px] leading-snug text-neutral-500">{d.detail}</div>
                    </div>
                  </div>
                ))}
              </div>
            </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
