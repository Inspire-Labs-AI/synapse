"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";

import {
  deleteRepo,
  fetchFileSummary,
  fetchGraph,
  fetchPathway,
  fetchRepos,
  type AxonStep,
  type BlueprintResponse,
  type GraphData,
  type QueryAnswer,
  type RepoInfo,
} from "./lib/api";
import GraphView from "./components/GraphView";
import ChatPanel from "./components/ChatPanel";
import ExecutionDock from "./components/ExecutionDock";
import BlueprintPanel from "./components/BlueprintPanel";
import RepoSelector from "./components/RepoSelector";
import TourOverlay from "./components/TourOverlay";
import TourLoading from "./components/TourLoading";

type Tab = "assistant" | "blueprint";

export default function WorkspacePage() {
  const [graph, setGraph] = useState<GraphData | null>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");
  const [reloadKey, setReloadKey] = useState(0);

  const [repos, setRepos] = useState<RepoInfo[]>([]);
  const [activeRepo, setActiveRepo] = useState<string | null>(null);
  const [reposLoaded, setReposLoaded] = useState(false);

  const [tab, setTab] = useState<Tab>("assistant");
  const [highlightedFiles, setHighlightedFiles] = useState<string[]>([]);
  const [executionFlow, setExecutionFlow] = useState<string[]>([]);
  const [focusNonce, setFocusNonce] = useState(0);
  const [selectedLabel, setSelectedLabel] = useState<string | null>(null);
  const [blueprint, setBlueprint] = useState<BlueprintResponse | null>(null);
  // File detail panel: a short LLM summary of the clicked file.
  const [fileSummary, setFileSummary] = useState<{ path: string; text: string } | null>(null);
  const [summaryLoading, setSummaryLoading] = useState(false);
  const summaryReqRef = useRef(0);

  // Cortex Perspective (executive/synaptic) + Axon onboarding tour.
  const [perspective, setPerspective] = useState<"synaptic" | "executive">("synaptic");
  const [tour, setTour] = useState<AxonStep[] | null>(null);
  const [tourIndex, setTourIndex] = useState(0);
  const [tourBusy, setTourBusy] = useState(false);
  const [tourIntro, setTourIntro] = useState("");

  // Preselect the repo from the URL (?repo=) when arriving from docs/architecture.
  useEffect(() => {
    const r = new URLSearchParams(window.location.search).get("repo");
    if (r) setActiveRepo(r);
  }, []);

  // Load the repo list (on mount + manual reload), keeping/choosing an active repo.
  useEffect(() => {
    const ctrl = new AbortController();
    fetchRepos(ctrl.signal)
      .then((list) => {
        setRepos(list);
        setReposLoaded(true);
        setActiveRepo((prev) =>
          prev && list.some((r) => r.root_path === prev)
            ? prev
            : (list[0]?.root_path ?? null),
        );
        if (list.length === 0) {
          setGraph({ nodes: [], edges: [] });
          setStatus("ready");
        }
      })
      .catch((err) => {
        if (err?.name !== "AbortError") setStatus("error");
      });
    return () => ctrl.abort();
  }, [reloadKey]);

  // Load the graph for the active repo.
  useEffect(() => {
    if (!activeRepo) return;
    const ctrl = new AbortController();
    setStatus("loading");
    fetchGraph(activeRepo, ctrl.signal)
      .then((g) => {
        setGraph(g);
        setStatus("ready");
      })
      .catch((err) => {
        if (err?.name !== "AbortError") setStatus("error");
      });
    return () => ctrl.abort();
  }, [activeRepo, reloadKey]);

  const resetViews = useCallback(() => {
    setHighlightedFiles([]);
    setExecutionFlow([]);
    setBlueprint(null);
    setSelectedLabel(null);
    setFileSummary(null);
    setSummaryLoading(false);
    setTour(null);
  }, []);

  const handleSelectRepo = useCallback(
    (rp: string) => {
      setActiveRepo(rp);
      resetViews();
    },
    [resetViews],
  );

  const handleDeleteRepo = useCallback(
    async (rp: string) => {
      try {
        await deleteRepo(rp);
      } catch {
        /* fall through — the reload below reflects the true state */
      }
      resetViews();
      setActiveRepo((prev) => (prev === rp ? null : prev));
      setReloadKey((k) => k + 1);
    },
    [resetViews],
  );

  const handleResult = useCallback((answer: QueryAnswer) => {
    setHighlightedFiles(answer.highlighted_files ?? []);
    setExecutionFlow(answer.execution_flow ?? []);
    setFocusNonce((n) => n + 1);
  }, []);

  const handleSelectNode = useCallback((_: string | null, label: string | null) => {
    setSelectedLabel(label);
  }, []);

  // Clicking empty canvas clears the query spotlight so the graph reads neutral.
  const handleClearHighlight = useCallback(() => {
    setHighlightedFiles([]);
  }, []);

  // Focus a single file on the canvas (used when clicking a function in the chat).
  const handleFocusFile = useCallback((file: string) => {
    setHighlightedFiles([file]);
    setFocusNonce((n) => n + 1);
  }, []);

  // Clicking a file node loads its AI summary for the detail panel. A monotonic
  // request id guards against out-of-order responses when the user clicks
  // between files quickly.
  const handleExpandFile = useCallback(
    (path: string) => {
      const req = ++summaryReqRef.current;
      setFileSummary(null);
      setSummaryLoading(true);
      fetchFileSummary(activeRepo, path)
        .then((text) => {
          if (req !== summaryReqRef.current) return;
          setFileSummary({ path, text });
          setSummaryLoading(false);
        })
        .catch(() => {
          if (req === summaryReqRef.current) setSummaryLoading(false);
        });
    },
    [activeRepo],
  );

  // --- Axon onboarding tour ---------------------------------------------------
  const startTour = useCallback(() => {
    if (!activeRepo || tourBusy) return;
    setTourBusy(true);
    setPerspective("synaptic"); // the tour focuses individual files
    setTab("assistant"); // blueprint mode owns the canvas + blocks camera focus
    setBlueprint(null);
    fetchPathway(activeRepo)
      .then((p) => {
        if (p.steps.length) {
          setTourIntro(p.intro);
          setTour(p.steps);
          setTourIndex(0);
        }
      })
      .catch(() => {})
      .finally(() => setTourBusy(false));
  }, [activeRepo, tourBusy]);

  const exitTour = useCallback(() => {
    setTour(null);
    setHighlightedFiles([]);
  }, []);

  // Fly the camera + highlight to the current tour step.
  useEffect(() => {
    if (!tour || !tour[tourIndex]) return;
    setHighlightedFiles([tour[tourIndex].file]);
    setFocusNonce((n) => n + 1);
  }, [tour, tourIndex]);

  const tourStep = tour ? (tour[tourIndex] ?? null) : null;
  const nodeCount = graph?.nodes.length ?? 0;
  const edgeCount = graph?.edges.length ?? 0;

  const tabBtn = (id: Tab) =>
    `flex-1 px-3 py-2 text-[12px] font-medium transition-colors ${
      tab === id
        ? "border-b-2 border-accent text-neutral-100"
        : "border-b-2 border-transparent text-neutral-500 hover:text-neutral-300"
    }`;

  const cortexBtn = (mode: "executive" | "synaptic") =>
    `px-2 py-1 text-[10px] font-medium uppercase tracking-wider transition-colors ${
      perspective === mode ? "bg-accent/20 text-accent" : "text-neutral-500 hover:text-neutral-300"
    }`;
  const toolBtn =
    "rounded-md border border-panel-border bg-neutral-900 px-2 py-1 text-[11px] text-neutral-300 transition-colors hover:border-accent hover:text-accent disabled:opacity-40";

  return (
    <div className="flex h-full w-full flex-col">
      <header className="flex shrink-0 items-center justify-between border-b border-panel-border bg-panel px-5 py-3">
        <div className="flex items-center gap-3">
          <span className="inline-block h-2.5 w-2.5 rounded-full bg-accent shadow-[0_0_12px_2px_var(--color-accent)]" />
          <h1 className="font-mono text-sm font-semibold tracking-tight text-neutral-100">
            project<span className="text-accent">·</span>synapse
          </h1>
          <span className="rounded-full border border-panel-border px-2 py-0.5 text-[10px] uppercase tracking-widest text-neutral-500">
            workspace
          </span>
          <RepoSelector
            repos={repos}
            active={activeRepo}
            onSelect={handleSelectRepo}
            onDelete={handleDeleteRepo}
          />
        </div>
        <div className="flex items-center gap-2.5 font-mono text-[11px] text-neutral-500">
          {/* Cortex Perspective toggle */}
          <div
            className="flex items-center overflow-hidden rounded-md border border-panel-border"
            title="Cortex Perspective — Executive masks files, showing only the high-level structure"
          >
            <button onClick={() => setPerspective("executive")} className={cortexBtn("executive")}>
              Executive
            </button>
            <button onClick={() => setPerspective("synaptic")} className={cortexBtn("synaptic")}>
              Synaptic
            </button>
          </div>
          <button
            onClick={startTour}
            disabled={!activeRepo || tourBusy}
            className={toolBtn}
            title="Start onboarding tour along the Axon dependency pathway"
          >
            {tourBusy ? (
              <span className="flex items-center gap-1.5">
                <span className="inline-block h-2.5 w-2.5 animate-spin rounded-full border border-accent border-t-transparent" />
                Charting…
              </span>
            ) : (
              "▶ Tour"
            )}
          </button>
          {activeRepo && (
            <div className="flex items-center overflow-hidden rounded-md border border-panel-border">
              {(
                [
                  ["Docs", `/docs?repo=${encodeURIComponent(activeRepo)}`],
                  ["Arch", `/architecture?repo=${encodeURIComponent(activeRepo)}`],
                  ["✂ Prune", `/prune?repo=${encodeURIComponent(activeRepo)}`],
                ] as const
              ).map(([label, href]) => (
                <Link
                  key={label}
                  href={href}
                  className="px-2 py-1 text-[10px] font-medium text-neutral-400 transition-colors hover:bg-neutral-800/60 hover:text-neutral-100"
                >
                  {label}
                </Link>
              ))}
            </div>
          )}
          <span className="ml-1">
            {nodeCount} nodes · {edgeCount} edges
          </span>
          <span className="flex items-center gap-1.5">
            <span
              className={
                "inline-block h-1.5 w-1.5 rounded-full " +
                (status === "ready"
                  ? "bg-emerald-400"
                  : status === "loading"
                    ? "bg-amber-400"
                    : "bg-red-400")
              }
            />
            {status === "ready" ? "connected" : status === "loading" ? "loading" : "offline"}
          </span>
        </div>
      </header>

      <main className="flex min-h-0 flex-1">
        <section className="relative min-w-0 basis-3/5 border-r border-panel-border">
          {status === "error" ? (
            <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
              <p className="font-mono text-sm text-neutral-400">Could not reach the graph API.</p>
              <p className="max-w-sm text-xs text-neutral-600">
                Start the backend on <span className="font-mono">:8080</span>, then retry.
              </p>
              <button
                onClick={() => setReloadKey((k) => k + 1)}
                className="rounded-lg border border-panel-border bg-neutral-900 px-3 py-1.5 text-xs text-neutral-200 transition-colors hover:border-accent"
              >
                Retry
              </button>
            </div>
          ) : reposLoaded && repos.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
              <p className="font-mono text-sm text-neutral-400">No repositories ingested yet.</p>
              <p className="max-w-sm text-xs text-neutral-600">
                Clone one with <span className="font-mono">POST /api/ingest</span>, or set{" "}
                <span className="font-mono">SYNAPSE_INGEST_ROOT</span> and restart the backend.
              </p>
            </div>
          ) : (
            <>
              <GraphView
                graph={graph}
                highlightedFiles={highlightedFiles}
                focusNonce={focusNonce}
                onSelectNode={handleSelectNode}
                blueprint={tab === "blueprint" ? blueprint : null}
                onExpandFile={handleExpandFile}
                onClearHighlight={handleClearHighlight}
                tourActive={tourBusy || tour !== null}
                fileSummary={fileSummary}
                summaryLoading={summaryLoading}
                perspective={perspective}
              />

              {/* Computing the pathway (LLM pass) — keep the user informed */}
              {tourBusy && !tour && <TourLoading />}

              {/* Onboarding (Axon) tour overlay */}
              {tour && tourStep && (
                <TourOverlay
                  steps={tour}
                  index={tourIndex}
                  intro={tourIntro}
                  onPrev={() => setTourIndex((i) => Math.max(0, i - 1))}
                  onNext={() => setTourIndex((i) => Math.min(tour.length - 1, i + 1))}
                  onJump={(i) => setTourIndex(i)}
                  onExit={exitTour}
                  onFocusFile={handleFocusFile}
                />
              )}
            </>
          )}
        </section>

        <aside className="flex min-h-0 min-w-0 basis-2/5 flex-col overflow-hidden bg-panel">
          <div className="flex shrink-0 border-b border-panel-border">
            <button className={tabBtn("assistant")} onClick={() => setTab("assistant")}>
              Assistant
            </button>
            <button className={tabBtn("blueprint")} onClick={() => setTab("blueprint")}>
              Blueprint
            </button>
          </div>

          <div className="relative min-h-0 flex-1">
            <div className={tab === "assistant" ? "h-full" : "hidden"}>
              <ChatPanel
                key={activeRepo ?? "none"}
                repo={activeRepo}
                onResult={handleResult}
                onFocusFile={handleFocusFile}
              />
            </div>
            <div className={tab === "blueprint" ? "h-full" : "hidden"}>
              <BlueprintPanel
                key={activeRepo ?? "none"}
                repo={activeRepo}
                onResult={setBlueprint}
              />
            </div>
          </div>

          {tab === "assistant" && (
            <ExecutionDock
              executionFlow={executionFlow}
              highlightedFiles={highlightedFiles}
              selectedLabel={selectedLabel}
            />
          )}
        </aside>
      </main>
    </div>
  );
}
