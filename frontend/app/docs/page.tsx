"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";

import {
  downloadJSON,
  exportFilename,
  fetchDocs,
  fetchProjectContext,
  type DocSection,
  type RepoDocs,
} from "../workspace/lib/api";
import Markdown, { slugify } from "../workspace/components/Markdown";

type TocItem = { id: string; text: string; level: number };

// One-line explainers shown under each sidebar group so their purpose is clear.
const GROUP_HINTS: Record<string, string> = {
  Overview: "The big picture — what this project is, how it's built, and how it works.",
  Subsystems: "Detailed, code-grounded deep-dive for each module in the codebase.",
};

export default function DocsPage() {
  const router = useRouter();
  const [repo, setRepo] = useState<string | null>(null);
  const [docs, setDocs] = useState<RepoDocs | null>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");
  const [activeId, setActiveId] = useState<string>("");
  const [toc, setToc] = useState<TocItem[]>([]);
  const contentRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setRepo(new URLSearchParams(window.location.search).get("repo"));
  }, []);

  useEffect(() => {
    if (repo === null) return;
    const ctrl = new AbortController();
    setStatus("loading");
    fetchDocs(repo, ctrl.signal)
      .then((d) => {
        setDocs(d);
        setActiveId(d.sections[0]?.id ?? "");
        setStatus("ready");
      })
      .catch((err) => {
        if (err?.name !== "AbortError") setStatus("error");
      });
    return () => ctrl.abort();
  }, [repo]);

  const active = useMemo(
    () => docs?.sections.find((s) => s.id === activeId) ?? null,
    [docs, activeId],
  );

  // Export a single self-contained brief of the project — the AST-derived facts
  // (entry points, endpoints, modules, hub files, live dependencies) plus these
  // docs — that another AI agent can be handed to understand the codebase.
  const [exporting, setExporting] = useState(false);
  async function exportContext() {
    if (!repo || exporting) return;
    setExporting(true);
    try {
      const ctx = await fetchProjectContext(repo);
      downloadJSON(exportFilename(ctx.name, "context"), ctx);
    } catch {
      // Surfaced inline on the button rather than as a blocking dialog.
    } finally {
      setExporting(false);
    }
  }

  // Build the right-hand TOC from the rendered section's headings.
  useEffect(() => {
    const el = contentRef.current;
    if (!active || !el) {
      setToc([]);
      return;
    }
    const heads = Array.from(el.querySelectorAll("h2, h3")) as HTMLElement[];
    setToc(
      heads.map((h) => {
        const id = slugify(h.textContent ?? "");
        h.id = id;
        return { id, text: h.textContent ?? "", level: h.tagName === "H2" ? 2 : 3 };
      }),
    );
  }, [active]);

  const groups = useMemo(() => {
    const m = new Map<string, DocSection[]>();
    for (const s of docs?.sections ?? []) {
      const arr = m.get(s.group) ?? [];
      arr.push(s);
      m.set(s.group, arr);
    }
    return Array.from(m.entries());
  }, [docs]);

  const repoQuery = repo ? `?repo=${encodeURIComponent(repo)}` : "";

  function selectSection(id: string) {
    setActiveId(id);
    contentRef.current?.closest("main")?.scrollTo({ top: 0 });
  }
  function scrollTo(id: string) {
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  return (
    <div className="flex h-full flex-col bg-background">
      {/* Top bar */}
      <header className="flex shrink-0 items-center justify-between border-b border-panel-border bg-panel px-5 py-3">
        <button onClick={() => router.push("/")} className="flex items-center gap-2.5">
          <span className="inline-block h-2.5 w-2.5 rounded-full bg-accent shadow-[0_0_12px_2px_var(--color-accent)]" />
          <span className="font-mono text-sm font-semibold tracking-tight text-neutral-100">
            project<span className="text-accent">·</span>synapse
          </span>
          {docs && (
            <span className="rounded-full border border-panel-border px-2 py-0.5 font-mono text-[11px] text-neutral-400">
              {docs.name}
            </span>
          )}
          <span className="text-[10px] uppercase tracking-widest text-neutral-600">docs</span>
        </button>
        <div className="flex items-center gap-2">
          <button
            onClick={exportContext}
            disabled={!repo || exporting}
            title="Download a single JSON brief of this project — architecture, entry points, endpoints, key files, dependencies, and these docs — to hand to any AI agent"
            className="rounded-lg border border-panel-border bg-neutral-900 px-3 py-1.5 text-[12px] text-neutral-200 transition-colors hover:border-emerald-400/60 hover:text-emerald-300 disabled:cursor-not-allowed disabled:opacity-40"
          >
            {exporting ? "Packaging…" : "↓ Export context"}
          </button>
          <button
            onClick={() => router.push(`/architecture${repoQuery}`)}
            className="rounded-lg border border-panel-border bg-neutral-900 px-3 py-1.5 text-[12px] text-neutral-200 transition-colors hover:border-neon hover:text-neon"
          >
            ◇ Architecture
          </button>
          <button
            onClick={() => router.push(`/prune${repoQuery}`)}
            className="rounded-lg border border-panel-border bg-neutral-900 px-3 py-1.5 text-[12px] text-neutral-200 transition-colors hover:border-red-400/60 hover:text-red-300"
          >
            ✂ Prune
          </button>
          <button
            onClick={() => router.push(`/workspace${repoQuery}`)}
            className="rounded-lg bg-accent px-3 py-1.5 text-[12px] font-medium text-white transition-colors hover:bg-indigo-500"
          >
            Workspace →
          </button>
        </div>
      </header>

      {status === "loading" && (
        <div className="flex flex-1 items-center justify-center gap-1.5">
          <span className="dot-pulse h-2 w-2 rounded-full bg-accent" />
          <span className="dot-pulse h-2 w-2 rounded-full bg-accent [animation-delay:0.15s]" />
          <span className="dot-pulse h-2 w-2 rounded-full bg-accent [animation-delay:0.3s]" />
          <span className="ml-2 font-mono text-xs text-neutral-500">generating documentation…</span>
        </div>
      )}

      {status === "error" && (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 text-center">
          <p className="font-mono text-sm text-neutral-400">Could not load documentation.</p>
          <p className="max-w-sm text-xs text-neutral-600">
            Make sure the backend is running on <span className="font-mono">:8080</span> and a repo is selected.
          </p>
          <button
            onClick={() => router.push("/")}
            className="rounded-lg border border-panel-border bg-neutral-900 px-3 py-1.5 text-xs text-neutral-200 hover:border-accent"
          >
            Back to home
          </button>
        </div>
      )}

      {status === "ready" && docs && (
        <div className="flex min-h-0 flex-1">
          {/* Left section nav */}
          <nav className="w-60 shrink-0 overflow-y-auto border-r border-panel-border bg-panel/40 px-3 py-4">
            {groups.map(([group, sections]) => (
              <div key={group} className="mb-4">
                <div className="mb-1.5 px-2">
                  <div className="text-[10px] font-semibold uppercase tracking-widest text-neutral-600">
                    {group}
                  </div>
                  {GROUP_HINTS[group] && (
                    <div className="mt-0.5 text-[10px] leading-snug text-neutral-700">{GROUP_HINTS[group]}</div>
                  )}
                </div>
                {sections.map((s) => (
                  <button
                    key={s.id}
                    onClick={() => selectSection(s.id)}
                    title={s.title}
                    className={
                      "block w-full truncate rounded-md px-2 py-1.5 text-left font-mono text-[12px] transition-colors " +
                      (s.id === activeId
                        ? "bg-accent/15 text-accent"
                        : "text-neutral-400 hover:bg-neutral-800/50 hover:text-neutral-200")
                    }
                  >
                    {s.title}
                  </button>
                ))}
              </div>
            ))}
          </nav>

          {/* Main content */}
          <main className="min-w-0 flex-1 overflow-y-auto">
            <div ref={contentRef} className="mx-auto max-w-3xl px-8 py-8">
              <Markdown>{active?.content ?? ""}</Markdown>
            </div>
          </main>

          {/* Right TOC */}
          <aside className="hidden w-52 shrink-0 overflow-y-auto border-l border-panel-border px-4 py-6 lg:block">
            {toc.length > 0 && (
              <>
                <div className="mb-2 text-[10px] font-semibold uppercase tracking-widest text-neutral-600">
                  On this page
                </div>
                <div className="space-y-1">
                  {toc.map((t) => (
                    <button
                      key={t.id}
                      onClick={() => scrollTo(t.id)}
                      className={
                        "block w-full truncate text-left text-[11px] text-neutral-500 transition-colors hover:text-accent " +
                        (t.level === 3 ? "pl-3" : "")
                      }
                      title={t.text}
                    >
                      {t.text}
                    </button>
                  ))}
                </div>
              </>
            )}
          </aside>
        </div>
      )}
    </div>
  );
}
