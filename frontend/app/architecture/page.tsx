"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Edge,
  type Node,
  type NodeProps,
  type NodeTypes,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";

import {
  fetchArchitecture,
  fetchFileFunctions,
  type FunctionHit,
  type RepoArchitecture,
} from "../workspace/lib/api";
import Markdown from "../workspace/components/Markdown";

const LAYER_ORDER = ["frontend", "shared", "backend", "data", "external"];
const LAYER_COLOR: Record<string, string> = {
  frontend: "#22d3ee",
  backend: "#6366f1",
  data: "#a855f7",
  external: "#52525b",
  shared: "#34d399",
};

// Layout geometry. Columns are laid out by layer; rows are stacked from each
// node's MEASURED height so an expanded card pushes its lower siblings down
// instead of overlapping them.
const COL_WIDTH = 360;
const ROW_GAP = 30;
const FALLBACK_H = 132; // collapsed-card height used until React Flow measures.

interface ArchNodeData extends Record<string, unknown> {
  name: string;
  layer: string;
  description: string;
  tech: string[];
  files: string[];
  order: number;
  repo: string | null;
  color: string;
  onResize: () => void; // ask the page to reflow after our height changes
}

function shortPath(p: string): string {
  const parts = p.split("/");
  return parts.length <= 2 ? p : `…/${parts.slice(-2).join("/")}`;
}

// Doc sections (Myelin markdown chunks) get a paragraph glyph; code symbols get ƒ.
function isDocChunk(t: string): boolean {
  return t === "myelin_doc";
}

// A component card that reveals its contents one layer at a time:
//   component → files → functions → source code.
// Functions are fetched lazily (only when a file is opened) so big systems stay
// responsive; results are cached per file on the node.
function ArchNode({ data }: NodeProps) {
  const d = data as ArchNodeData;
  const color = d.color;

  const [open, setOpen] = useState(false);
  const [openFiles, setOpenFiles] = useState<Set<string>>(new Set());
  const [openCode, setOpenCode] = useState<Set<string>>(new Set());
  const [funcs, setFuncs] = useState<Record<string, FunctionHit[]>>({});
  const [loading, setLoading] = useState<Set<string>>(new Set());

  // Whenever our visible content changes, let the page restack the column.
  useEffect(() => {
    d.onResize();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, openFiles, openCode, funcs, loading]);

  const toggleFile = useCallback(
    async (path: string) => {
      let willOpen = false;
      setOpenFiles((cur) => {
        const next = new Set(cur);
        if (next.has(path)) next.delete(path);
        else {
          next.add(path);
          willOpen = true;
        }
        return next;
      });
      if (willOpen && funcs[path] === undefined && !loading.has(path)) {
        setLoading((l) => new Set(l).add(path));
        try {
          const { functions: hits } = await fetchFileFunctions(d.repo, path);
          setFuncs((f) => ({ ...f, [path]: hits }));
        } catch {
          setFuncs((f) => ({ ...f, [path]: [] }));
        } finally {
          setLoading((l) => {
            const n = new Set(l);
            n.delete(path);
            return n;
          });
        }
      }
    },
    [d.repo, funcs, loading],
  );

  const toggleCode = useCallback((key: string) => {
    setOpenCode((cur) => {
      const n = new Set(cur);
      if (n.has(key)) n.delete(key);
      else n.add(key);
      return n;
    });
  }, []);

  return (
    <div className="synapse-card w-[300px] rounded-xl border bg-panel" style={{ borderColor: color }}>
      <Handle type="target" position={Position.Left} className="!h-1.5 !w-1.5 !border-0 !bg-zinc-600" />

      {/* Layer 0 — the component header (click to reveal its files). */}
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center justify-between gap-2 border-b px-3 py-2 text-left transition-colors hover:bg-neutral-800/40"
        style={{ borderColor: color + "40" }}
      >
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="text-[10px] text-neutral-500">{open ? "▾" : "▸"}</span>
          <span className="truncate font-mono text-[13px] font-semibold text-neutral-100">{d.name}</span>
        </span>
        <span
          className="ml-2 shrink-0 rounded px-1.5 py-0.5 text-[9px] font-bold uppercase"
          style={{ backgroundColor: color, color: "#0a0a0a" }}
        >
          {d.layer}
        </span>
      </button>

      <div className="px-3 py-2">
        <p className="text-[11px] leading-snug text-neutral-400">{d.description}</p>
        {d.tech.length > 0 && (
          <div className="mt-1.5 flex flex-wrap gap-1">
            {d.tech.slice(0, 6).map((t) => (
              <span
                key={t}
                className="rounded border px-1.5 py-0.5 font-mono text-[9px]"
                style={{ borderColor: color + "55", color: color }}
              >
                {t}
              </span>
            ))}
          </div>
        )}
        <div className="mt-1.5 text-[10px] text-neutral-600">
          {d.files.length} file{d.files.length === 1 ? "" : "s"} · click to {open ? "collapse" : "expand"}
        </div>
      </div>

      {/* Layer 1 — files. `nowheel` lets the inner list scroll without zooming. */}
      {open && (
        <div
          className="nowheel max-h-[360px] overflow-y-auto border-t px-2 py-2"
          style={{ borderColor: color + "30" }}
        >
          {d.files.length === 0 && (
            <div className="px-1 py-1 text-[11px] text-neutral-600">No files mapped to this component.</div>
          )}
          {d.files.map((path) => {
            const fileOpen = openFiles.has(path);
            const hits = funcs[path];
            const isLoading = loading.has(path);
            return (
              <div key={path} className="mb-0.5">
                <button
                  onClick={() => toggleFile(path)}
                  className="flex w-full items-center gap-1.5 rounded px-1.5 py-1 text-left transition-colors hover:bg-neutral-800/60"
                  title={path}
                >
                  <span className="text-[9px] text-neutral-500">{fileOpen ? "▾" : "▸"}</span>
                  <span className="truncate font-mono text-[11px] text-neutral-300">{shortPath(path)}</span>
                  {hits !== undefined && (
                    <span className="ml-auto shrink-0 text-[9px] text-neutral-600">
                      {hits.length} {hits.length > 0 && isDocChunk(hits[0].chunk_type) ? "¶" : "ƒ"}
                    </span>
                  )}
                </button>

                {/* Layer 2 — functions inside the file. */}
                {fileOpen && (
                  <div className="ml-3 border-l border-neutral-800 pl-2">
                    {isLoading && <div className="py-1 text-[10px] text-neutral-600">loading functions…</div>}
                    {!isLoading && hits && hits.length === 0 && (
                      <div className="py-1 text-[10px] text-neutral-600">No functions parsed.</div>
                    )}
                    {hits?.map((fn) => {
                      const key = `${path}::${fn.symbol}::${fn.start_line}`;
                      const codeOpen = openCode.has(key);
                      const doc = isDocChunk(fn.chunk_type);
                      return (
                        <div key={key} className="py-0.5">
                          <button
                            onClick={() => toggleCode(key)}
                            className="flex w-full items-center gap-1.5 rounded px-1 py-0.5 text-left transition-colors hover:bg-neutral-800/60"
                          >
                            <span
                              className="shrink-0 font-mono text-[11px]"
                              style={{ color: doc ? "#a855f7" : "var(--color-accent)" }}
                            >
                              {doc ? "¶" : "ƒ"}
                            </span>
                            <span className="truncate font-mono text-[11px] text-neutral-200">{fn.symbol}</span>
                            <span className="ml-auto shrink-0 text-[9px] text-neutral-600">
                              L{fn.start_line}
                            </span>
                          </button>
                          {/* Layer 3 — the source itself. */}
                          {codeOpen && (
                            <pre className="nowheel mt-1 max-h-44 overflow-auto rounded bg-neutral-950 p-2 text-[10px] leading-relaxed text-neutral-300">
                              <code>{fn.code}</code>
                            </pre>
                          )}
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}

      <Handle type="source" position={Position.Right} className="!h-1.5 !w-1.5 !border-0 !bg-zinc-600" />
    </div>
  );
}

const nodeTypes: NodeTypes = { archComponent: ArchNode };

// Stack each column from measured heights so expanded cards never overlap.
// Nodes the user has dragged (`pinned`) keep their manual position and are
// skipped, so auto-stacking never snaps a moved card back.
function relayout(nodes: Node[], pinned: Set<string>): Node[] {
  const layers: string[] = [];
  for (const l of LAYER_ORDER) if (nodes.some((n) => (n.data as ArchNodeData).layer === l)) layers.push(l);
  for (const n of nodes) {
    const l = (n.data as ArchNodeData).layer;
    if (!layers.includes(l)) layers.push(l);
  }
  const colIndex = new Map(layers.map((l, i) => [l, i]));

  const byCol = new Map<number, Node[]>();
  for (const n of nodes) {
    const col = colIndex.get((n.data as ArchNodeData).layer) ?? 0;
    const arr = byCol.get(col) ?? [];
    arr.push(n);
    byCol.set(col, arr);
  }

  const pos = new Map<string, { x: number; y: number }>();
  for (const [col, arr] of byCol) {
    arr.sort((a, b) => (a.data as ArchNodeData).order - (b.data as ArchNodeData).order);
    let y = 0;
    for (const n of arr) {
      if (pinned.has(n.id)) continue; // leave dragged cards where the user put them
      pos.set(n.id, { x: col * COL_WIDTH, y });
      y += (n.measured?.height ?? FALLBACK_H) + ROW_GAP;
    }
  }
  return nodes.map((n) => (pinned.has(n.id) ? n : { ...n, position: pos.get(n.id) ?? n.position }));
}

export default function ArchitecturePage() {
  const router = useRouter();
  const [repo, setRepo] = useState<string | null>(null);
  const [arch, setArch] = useState<RepoArchitecture | null>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");

  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([]);
  const [reflowTick, setReflowTick] = useState(0);
  const requestReflow = useCallback(() => setReflowTick((t) => t + 1), []);
  const repoRef = useRef<string | null>(null);
  repoRef.current = repo;
  // Nodes the user has dragged — the auto-stacker leaves these where they put them.
  const pinnedRef = useRef<Set<string>>(new Set());
  const retidy = useCallback(() => {
    pinnedRef.current = new Set();
    requestReflow();
  }, [requestReflow]);

  useEffect(() => {
    setRepo(new URLSearchParams(window.location.search).get("repo"));
  }, []);

  useEffect(() => {
    if (repo === null) return;
    const ctrl = new AbortController();
    setStatus("loading");
    fetchArchitecture(repo, ctrl.signal)
      .then((a) => {
        setArch(a);
        setStatus("ready");
      })
      .catch((err) => {
        if (err?.name !== "AbortError") setStatus("error");
      });
    return () => ctrl.abort();
  }, [repo]);

  // Build the graph once the architecture loads.
  useEffect(() => {
    if (!arch) return;
    const layers = LAYER_ORDER.filter((l) => arch.components.some((c) => c.layer === l));
    for (const c of arch.components) if (!layers.includes(c.layer)) layers.push(c.layer);
    const colIndex = new Map(layers.map((l, i) => [l, i]));
    const rowOf = new Map<string, number>();

    const nextNodes: Node[] = arch.components.map((c) => {
      const col = colIndex.get(c.layer) ?? 0;
      const row = rowOf.get(c.layer) ?? 0;
      rowOf.set(c.layer, row + 1);
      return {
        id: c.id,
        type: "archComponent",
        position: { x: col * COL_WIDTH, y: row * 170 },
        data: {
          name: c.name,
          layer: c.layer,
          description: c.description,
          tech: c.tech,
          files: c.files,
          order: row,
          repo: repoRef.current,
          color: LAYER_COLOR[c.layer] ?? "#94a3b8",
          onResize: requestReflow,
        } satisfies ArchNodeData,
      };
    });

    const nextEdges: Edge[] = arch.edges.map((e, i) => ({
      id: `ae-${e.source}-${e.target}-${i}`,
      source: e.source,
      target: e.target,
      label: e.label,
      type: "smoothstep",
      animated: true,
      markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color: "#6366f1" },
      style: { stroke: "#6366f1", strokeWidth: 1.5 },
      labelStyle: { fill: "#a1a1aa", fontSize: 10, fontFamily: "var(--font-mono)" },
      labelBgStyle: { fill: "#111113" },
    }));

    pinnedRef.current = new Set(); // fresh graph → clear any manual positions
    setNodes(nextNodes);
    setEdges(nextEdges);
    // Tighten the columns once React Flow has measured the cards.
    requestReflow();
  }, [arch, setNodes, setEdges, requestReflow]);

  // Reflow shortly after any height change so measurements have settled.
  useEffect(() => {
    if (status !== "ready") return;
    const t = setTimeout(() => setNodes((cur) => relayout(cur, pinnedRef.current)), 60);
    return () => clearTimeout(t);
  }, [reflowTick, status, setNodes]);

  const repoQuery = repo ? `?repo=${encodeURIComponent(repo)}` : "";
  const compName = useMemo(
    () => new Map((arch?.components ?? []).map((c) => [c.id, c.name] as const)),
    [arch],
  );

  return (
    <div className="flex h-full flex-col bg-background">
      <header className="flex shrink-0 items-center justify-between border-b border-panel-border bg-panel px-5 py-3">
        <button onClick={() => router.push("/")} className="flex items-center gap-2.5">
          <span className="inline-block h-2.5 w-2.5 rounded-full bg-neon shadow-[0_0_12px_2px_var(--color-neon)]" />
          <span className="font-mono text-sm font-semibold tracking-tight text-neutral-100">
            project<span className="text-accent">·</span>synapse
          </span>
          {arch && (
            <span className="rounded-full border border-panel-border px-2 py-0.5 font-mono text-[11px] text-neutral-400">
              {arch.name}
            </span>
          )}
          <span className="text-[10px] uppercase tracking-widest text-neutral-600">architecture</span>
        </button>
        <div className="flex items-center gap-2">
          <button
            onClick={() => router.push(`/docs${repoQuery}`)}
            className="rounded-lg border border-panel-border bg-neutral-900 px-3 py-1.5 text-[12px] text-neutral-200 transition-colors hover:border-accent hover:text-accent"
          >
            ← Docs
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
          <span className="dot-pulse h-2 w-2 rounded-full bg-neon" />
          <span className="dot-pulse h-2 w-2 rounded-full bg-neon [animation-delay:0.15s]" />
          <span className="dot-pulse h-2 w-2 rounded-full bg-neon [animation-delay:0.3s]" />
          <span className="ml-2 font-mono text-xs text-neutral-500">analyzing architecture…</span>
        </div>
      )}

      {status === "error" && (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 text-center">
          <p className="font-mono text-sm text-neutral-400">Could not load the architecture view.</p>
          <button
            onClick={() => router.push(`/docs${repoQuery}`)}
            className="rounded-lg border border-panel-border bg-neutral-900 px-3 py-1.5 text-xs text-neutral-200 hover:border-accent"
          >
            Back to docs
          </button>
        </div>
      )}

      {status === "ready" && arch && (
        <main className="flex min-h-0 flex-1">
          <section className="relative min-w-0 flex-1">
            <ReactFlow
              nodes={nodes}
              edges={edges}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              onNodeDragStart={(_, node) => pinnedRef.current.add(node.id)}
              nodeTypes={nodeTypes}
              colorMode="dark"
              fitView
              minZoom={0.2}
              maxZoom={1.8}
              proOptions={{ hideAttribution: true }}
            >
              <Background variant={BackgroundVariant.Dots} gap={24} size={1} color="#26262b" />
              <Controls showInteractive={false} className="!border-panel-border !bg-panel" />
              <MiniMap
                pannable
                maskColor="rgba(10,10,10,0.75)"
                nodeColor={(n) => LAYER_COLOR[String((n.data as { layer?: string }).layer)] ?? "#3b82f6"}
                style={{ background: "#111113", border: "1px solid #26262b" }}
              />
            </ReactFlow>
          </section>

          {/* Side panel: the actual system-design explanation. */}
          <aside className="flex w-80 shrink-0 flex-col overflow-y-auto border-l border-panel-border bg-panel">
            <div className="border-b border-panel-border px-4 py-3">
              <div className="text-[10px] uppercase tracking-widest text-neutral-500">system overview</div>
              {arch.pattern && (
                <div className="mt-2 inline-flex flex-wrap items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-2 py-1 text-[11px] text-accent">
                  <span className="text-[9px] uppercase tracking-wider opacity-70">pattern</span>
                  <span className="font-medium">{arch.pattern}</span>
                </div>
              )}
              <p className="mt-2 text-[13px] leading-relaxed text-neutral-300">{arch.summary}</p>
            </div>

            {arch.design && (
              <div className="border-b border-panel-border px-4 py-3">
                <div className="mb-1 text-[10px] uppercase tracking-widest text-neutral-500">system design</div>
                <Markdown className="text-[12.5px]">{arch.design}</Markdown>
              </div>
            )}

            {arch.edges.length > 0 && (
              <div className="border-b border-panel-border px-4 py-3">
                <div className="mb-2 text-[10px] uppercase tracking-widest text-neutral-500">
                  data flows · {arch.edges.length}
                </div>
                <div className="space-y-2">
                  {arch.edges.map((e, i) => (
                    <div key={i} className="rounded-md border border-panel-border bg-neutral-900/40 px-2.5 py-1.5">
                      <div className="flex items-center gap-1.5 font-mono text-[11px] text-neutral-200">
                        <span className="truncate">{compName.get(e.source) ?? e.source}</span>
                        <span className="shrink-0 text-accent">→</span>
                        <span className="truncate">{compName.get(e.target) ?? e.target}</span>
                        {e.label && (
                          <span className="ml-auto shrink-0 rounded bg-neutral-800 px-1.5 py-0.5 text-[9px] text-neutral-400">
                            {e.label}
                          </span>
                        )}
                      </div>
                      {e.description && (
                        <p className="mt-1 text-[10.5px] leading-snug text-neutral-500">{e.description}</p>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            )}

            <div className="border-b border-panel-border px-4 py-3">
              <div className="mb-2 text-[10px] uppercase tracking-widest text-neutral-500">
                layers · {arch.components.length} components
              </div>
              <div className="space-y-1.5">
                {LAYER_ORDER.filter((l) => arch.components.some((c) => c.layer === l)).map((layer) => {
                  const color = LAYER_COLOR[layer] ?? "#94a3b8";
                  const count = arch.components.filter((c) => c.layer === layer).length;
                  return (
                    <div key={layer} className="flex items-center gap-2">
                      <span className="h-2.5 w-2.5 shrink-0 rounded-full" style={{ backgroundColor: color }} />
                      <span className="font-mono text-[12px] capitalize text-neutral-200">{layer}</span>
                      <span className="ml-auto text-[10px] text-neutral-600">{count}</span>
                    </div>
                  );
                })}
              </div>
            </div>

            <div className="px-4 py-3">
              <div className="mb-2 flex items-center justify-between">
                <div className="text-[10px] uppercase tracking-widest text-neutral-500">interact</div>
                <button
                  onClick={retidy}
                  title="Reset any dragged cards and re-stack the layout"
                  className="rounded border border-panel-border px-1.5 py-0.5 text-[10px] text-neutral-400 transition-colors hover:border-neon hover:text-neon"
                >
                  ⤢ re-tidy
                </button>
              </div>
              <ol className="space-y-1 text-[11px] leading-relaxed text-neutral-400">
                <li><span className="text-neutral-200">Drag a card</span> → reposition it freely.</li>
                <li><span className="text-neutral-200">Click a component</span> → reveals its files.</li>
                <li><span className="text-neutral-200">Click a file</span> → reveals its functions.</li>
                <li><span className="text-neutral-200">Click a function</span> → reveals its source.</li>
              </ol>
            </div>
          </aside>
        </main>
      )}
    </div>
  );
}
