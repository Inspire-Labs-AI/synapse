"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import * as d3 from "d3";

import type { BlueprintResponse, GraphData } from "../lib/api";

type Layout = "galaxy" | "radial";

interface GraphViewProps {
  graph: GraphData | null;
  highlightedFiles: string[];
  focusNonce: number;
  onSelectNode: (id: string | null, label: string | null) => void;
  blueprint?: BlueprintResponse | null;
  onExpandFile?: (path: string) => void;
  onClearHighlight?: () => void;
  tourActive?: boolean;
  fileSummary?: { path: string; text: string } | null;
  summaryLoading?: boolean;
  perspective?: "synaptic" | "executive";
}

// A query returns its whole retrieval set; highlighting all of it drowns the
// signal, so we light up only the top-ranked (most relevant) files.
const MAX_HIGHLIGHT = 6;

interface GNode extends d3.SimulationNodeDatum {
  id: string; label: string; kind: string; path?: string;
  folder: string; degree: number; r: number; color: string;
  alpha?: number; // animated opacity, eased toward its lit/dim target each frame
}
interface GLink extends d3.SimulationLinkDatum<GNode> { source: string | GNode; target: string | GNode; }
type Selected = { id: string; label: string; path?: string; kind: string };
type HoverInfo = { label: string; folder: string; color: string; imports: number; importers: number; sx: number; sy: number } | null;

const dirOf = (p: string) => { const i = p.replace(/\\/g, "/").lastIndexOf("/"); return i >= 0 ? p.slice(0, i) : ""; };
const baseOf = (p: string) => { const s = p.replace(/\\/g, "/"); const i = s.lastIndexOf("/"); return i >= 0 ? s.slice(i + 1) : s; };
const groupOf = (p: string) => {
  const parts = p.replace(/\\/g, "/").split("/").filter(Boolean);
  if (parts.length <= 1) return "(root)";
  if (parts.length === 2) return parts[0];
  return parts[0] + "/" + parts[1];
};

const PALETTE = ["#818cf8", "#38bdf8", "#34d399", "#fbbf24", "#fb7185", "#c084fc", "#2dd4bf", "#f97316", "#a3e635", "#e879f9", "#60a5fa", "#4ade80"];
const MODULE_COLOR = "#6b7280";
const ENDPOINT_COLOR = "#f59e0b";
const OUT_COLOR = "#fbbf24"; // imports (outgoing)
const IN_COLOR = "#38bdf8"; // imported-by (incoming)

export default function GraphView({
  graph, highlightedFiles, focusNonce, onSelectNode, onExpandFile, onClearHighlight, tourActive = false,
  fileSummary, summaryLoading = false, perspective = "synaptic",
}: GraphViewProps) {
  const [layout, setLayout] = useState<Layout>("galaxy");
  const [search, setSearch] = useState("");
  const [searchOpen, setSearchOpen] = useState(false);
  const [selected, setSelected] = useState<Selected | null>(null);
  const [hoverInfo, setHoverInfo] = useState<HoverInfo>(null);
  const [ready, setReady] = useState(false);
  const [filterGroup, setFilterGroup] = useState<string | null>(null);

  const wrapRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  const interactRef = useRef({ hover: null as string | null, pinned: null as string | null, focusPin: null as string | null, highlight: new Set<string>(), filter: null as Set<string> | null });
  const kickRef = useRef<() => void>(() => {});
  const focusRef = useRef<(ids: string[], yFrac?: number) => void>(() => {});
  const navRef = useRef<{ zoomIn: () => void; zoomOut: () => void; fit: () => void; reset: () => void } | null>(null);

  // --- build nodes + links ---------------------------------------------------
  const { nodes, links, folders } = useMemo(() => {
    if (!graph) return { nodes: [] as GNode[], links: [] as GLink[], folders: [] as string[] };
    const raw = graph.nodes;
    const byId = new Map(raw.map((n) => [n.id, n]));
    const deg = new Map<string, number>();
    const validLinks: GLink[] = [];
    for (const e of graph.edges) {
      if (!byId.has(e.source) || !byId.has(e.target) || e.source === e.target) continue;
      deg.set(e.source, (deg.get(e.source) ?? 0) + 1);
      deg.set(e.target, (deg.get(e.target) ?? 0) + 1);
      validLinks.push({ source: e.source, target: e.target });
    }
    const folderSet = new Set<string>();
    for (const n of raw) if (String(n.data.kind ?? "file") === "file" && n.data.path) folderSet.add(groupOf(String(n.data.path)));
    const folderList = [...folderSet].sort();
    const colorForFolder = (f: string) => PALETTE[folderList.indexOf(f) % PALETTE.length];

    let simNodes: GNode[] = raw.map((n) => {
      const kind = String(n.data.kind ?? "file");
      const path = n.data.path ? String(n.data.path) : undefined;
      const folder = path ? groupOf(path) : kind;
      const degree = deg.get(n.id) ?? 0;
      const color = kind === "module" ? MODULE_COLOR : kind === "endpoint" ? ENDPOINT_COLOR : colorForFolder(folder);
      return { id: n.id, label: String(n.data.label ?? baseOf(path ?? n.id)), kind, path, folder, degree, r: Math.min(16, 3 + Math.sqrt(degree) * 1.6), color };
    });
    let simLinks = validLinks;

    if (perspective === "executive") {
      const folderNode = new Map<string, GNode>(); const fileFolder = new Map<string, string>();
      for (const n of simNodes) {
        if (n.kind !== "file") continue;
        fileFolder.set(n.id, n.folder);
        if (!folderNode.has(n.folder)) folderNode.set(n.folder, { id: `folder:${n.folder}`, label: n.folder, kind: "folder", folder: n.folder, degree: 0, r: 8, color: colorForFolder(n.folder) });
      }
      const fedge = new Map<string, number>();
      for (const l of simLinks) { const sf = fileFolder.get(l.source as string), tf = fileFolder.get(l.target as string); if (!sf || !tf || sf === tf) continue; fedge.set(`folder:${sf} folder:${tf}`, (fedge.get(`folder:${sf} folder:${tf}`) ?? 0) + 1); }
      const counts = new Map<string, number>(); for (const f of fileFolder.values()) counts.set(f, (counts.get(f) ?? 0) + 1);
      for (const fn of folderNode.values()) fn.r = Math.min(26, 8 + Math.sqrt(counts.get(fn.folder) ?? 1) * 2);
      simNodes = [...folderNode.values()];
      simLinks = [...fedge.keys()].map((k) => { const [s, t] = k.split(" ") as [string, string]; return { source: s, target: t } as GLink; });
    }
    return { nodes: simNodes, links: simLinks, folders: folderList };
  }, [graph, perspective]);

  const nodeById = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  const neighbourMap = useMemo(() => {
    const m = new Map<string, Set<string>>();
    for (const l of links) { const s = typeof l.source === "string" ? l.source : l.source.id; const t = typeof l.target === "string" ? l.target : l.target.id; (m.get(s) ?? m.set(s, new Set()).get(s)!).add(t); (m.get(t) ?? m.set(t, new Set()).get(t)!).add(s); }
    return m;
  }, [links]);
  const dir = useMemo(() => {
    const out = new Map<string, string[]>(), inc = new Map<string, string[]>();
    for (const l of links) { const s = typeof l.source === "string" ? l.source : l.source.id; const t = typeof l.target === "string" ? l.target : l.target.id; (out.get(s) ?? out.set(s, []).get(s)!).push(t); (inc.get(t) ?? inc.set(t, []).get(t)!).push(s); }
    return { out, inc };
  }, [links]);

  const searchResults = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return [] as GNode[];
    return nodes.filter((n) => (n.path ?? n.label).toLowerCase().includes(q)).slice(0, 8);
  }, [search, nodes]);

  // --- main render effect ----------------------------------------------------
  useEffect(() => {
    const canvas = canvasRef.current, wrap = wrapRef.current;
    if (!canvas || !wrap || nodes.length === 0) return;
    setReady(false);
    const ctx = canvas.getContext("2d")!;
    let width = wrap.clientWidth, height = wrap.clientHeight;
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    let transform = d3.zoomIdentity;
    let alphaHot = false; // true while any node opacity is still easing to target

    type RadialLink = { pts: [number, number][]; a: string; b: string };
    let radialLinks: RadialLink[] = [];

    function buildRadial() {
      const fileNodes = nodes.filter((n) => n.kind === "file" && n.path);
      if (fileNodes.length === 0) return;
      const entries = new Map<string, { id: string; parent: string | null }>();
      entries.set("__root__", { id: "__root__", parent: null });
      const ensureFolder = (f: string): string => { if (f === "") return "__root__"; if (!entries.has("d:" + f)) entries.set("d:" + f, { id: "d:" + f, parent: ensureFolder(dirOf(f)) }); return "d:" + f; };
      for (const n of fileNodes) entries.set(n.id, { id: n.id, parent: ensureFolder(dirOf(n.path!)) });
      const root = d3.stratify<{ id: string; parent: string | null }>().id((d) => d.id).parentId((d) => d.parent)([...entries.values()]);
      const radius = Math.min(width, height) / 2 - 90;
      const laid = d3.cluster<{ id: string; parent: string | null }>().size([2 * Math.PI, radius])(root as unknown as d3.HierarchyNode<{ id: string; parent: string | null }>);
      const leafById = new Map<string, d3.HierarchyPointNode<{ id: string; parent: string | null }>>();
      laid.each((d) => { const a = d.x - Math.PI / 2; const gn = nodeById.get(d.data.id); if (gn) { gn.x = Math.cos(a) * d.y; gn.y = Math.sin(a) * d.y; } if (!d.data.id.startsWith("d:") && d.data.id !== "__root__") leafById.set(d.data.id, d); });
      radialLinks = []; const seen = new Set<string>();
      for (const l of links) {
        const s = typeof l.source === "string" ? l.source : l.source.id; const t = typeof l.target === "string" ? l.target : l.target.id;
        const a = leafById.get(s), b = leafById.get(t); if (!a || !b || s === t) continue;
        const key = s < t ? s + t : t + s; if (seen.has(key)) continue; seen.add(key);
        radialLinks.push({ pts: a.path(b).map((p) => { const ang = p.x - Math.PI / 2; return [Math.cos(ang) * p.y, Math.sin(ang) * p.y] as [number, number]; }), a: s, b: t });
      }
    }

    // flowing particles along a segment, direction A->B (imports flow outward)
    function flow(ax: number, ay: number, bx: number, by: number, color: string, now: number, phase: number) {
      const k = transform.k;
      for (let i = 0; i < 3; i++) {
        const t = ((now / 1500) + i / 3 + phase) % 1;
        const x = ax + (bx - ax) * t, y = ay + (by - ay) * t;
        ctx.beginPath(); ctx.arc(x, y, 2.2 / k, 0, 2 * Math.PI);
        ctx.fillStyle = color; ctx.globalAlpha = 0.95 * (1 - Math.abs(t - 0.5)); ctx.fill();
      }
      ctx.globalAlpha = 1;
    }

    function draw(now: number) {
      ctx.save();
      ctx.clearRect(0, 0, canvas!.width, canvas!.height);
      ctx.scale(dpr, dpr);
      ctx.translate(transform.x, transform.y);
      ctx.scale(transform.k, transform.k);
      const k = transform.k;

      const { hover, pinned, focusPin, highlight, filter } = interactRef.current;
      const focus = hover ?? pinned ?? focusPin;
      const nbr = focus ? neighbourMap.get(focus) ?? null : null;
      const focusActive = focus !== null || highlight.size > 0;
      const inFilter = (id: string) => filter === null || filter.has(id);
      const isLit = (id: string) => { if (!inFilter(id)) return false; return focusActive ? id === focus || (nbr?.has(id) ?? false) || highlight.has(id) : true; };
      const focusNode = focus ? nodeById.get(focus) : null;

      // links
      if (layout === "radial") {
        for (const onTop of focus ? [false, true] : [false]) {
          for (const rl of radialLinks) {
            const p = rl.pts; if (p.length < 2) continue;
            const isFocusArc = focus !== null && (rl.a === focus || rl.b === focus);
            if (onTop !== isFocusArc) continue;
            if (filter && !(filter.has(rl.a) || filter.has(rl.b))) continue;
            ctx.beginPath(); ctx.moveTo(p[0][0], p[0][1]);
            for (let i = 1; i < p.length - 1; i++) ctx.quadraticCurveTo(p[i][0], p[i][1], (p[i][0] + p[i + 1][0]) / 2, (p[i][1] + p[i + 1][1]) / 2);
            ctx.strokeStyle = isFocusArc ? (rl.a === focus ? OUT_COLOR : IN_COLOR) : `rgba(129,140,248,${focus ? 0.03 : focusActive ? 0.06 : 0.13})`;
            ctx.globalAlpha = isFocusArc ? 0.85 : 1; ctx.lineWidth = (isFocusArc ? 1.4 : 0.7) / k; ctx.stroke(); ctx.globalAlpha = 1;
            if (isFocusArc) { const mid = p[Math.floor(p.length / 2)] ?? p[0]; flow(p[0][0], p[0][1], mid[0], mid[1], rl.a === focus ? OUT_COLOR : IN_COLOR, now, (rl.a.length % 7) / 7); }
          }
        }
      } else {
        for (const l of links) {
          const s = l.source as GNode, t = l.target as GNode; if (s.x == null || t.x == null) continue;
          if (focus && (s.id === focus || t.id === focus)) continue;
          const lit = isLit(s.id) && isLit(t.id);
          ctx.beginPath(); ctx.moveTo(s.x, s.y!); ctx.lineTo(t.x, t.y!);
          ctx.strokeStyle = lit ? "rgba(165,165,190,0.22)" : "rgba(120,120,140,0.045)"; ctx.lineWidth = (lit ? 0.9 : 0.5) / k; ctx.stroke();
        }
        if (focusNode && focusNode.x != null) {
          for (const tid of dir.out.get(focus!) ?? []) { const n = nodeById.get(tid); if (n?.x == null) continue; ctx.beginPath(); ctx.moveTo(focusNode.x, focusNode.y!); ctx.lineTo(n.x, n.y!); ctx.strokeStyle = "rgba(251,191,36,0.5)"; ctx.lineWidth = 1.1 / k; ctx.stroke(); flow(focusNode.x, focusNode.y!, n.x, n.y!, OUT_COLOR, now, (tid.length % 7) / 7); }
          for (const sid of dir.inc.get(focus!) ?? []) { const n = nodeById.get(sid); if (n?.x == null) continue; ctx.beginPath(); ctx.moveTo(n.x, n.y!); ctx.lineTo(focusNode.x, focusNode.y!); ctx.strokeStyle = "rgba(56,189,248,0.5)"; ctx.lineWidth = 1.1 / k; ctx.stroke(); flow(n.x, n.y!, focusNode.x, focusNode.y!, IN_COLOR, now, (sid.length % 7) / 7); }
        }
      }

      // nodes — draw circles + rings in world space; collect label candidates
      // for a separate screen-space pass (constant size + de-cluttered).
      const showLabels = k > 1.5 || nodes.length < 120;
      const labelCands: { n: GNode; lit: boolean; pri: number }[] = [];
      alphaHot = false;
      for (const n of nodes) {
        if (n.x == null) continue;
        const lit = isLit(n.id);
        const emphasised = n.id === focus || n.id === pinned || highlight.has(n.id) || (nbr?.has(n.id) ?? false);
        // ease opacity toward its target so isolate / focus / highlight fade in
        // smoothly instead of snapping.
        const target = lit ? 1 : 0.13;
        n.alpha = n.alpha == null ? target : n.alpha + (target - n.alpha) * 0.2;
        if (Math.abs(target - n.alpha) > 0.006) alphaHot = true; else n.alpha = target;
        if (emphasised) { ctx.shadowColor = n.color; ctx.shadowBlur = (n.id === focus || n.id === pinned ? 16 : 8); }
        ctx.beginPath(); ctx.arc(n.x, n.y!, n.r, 0, 2 * Math.PI);
        ctx.fillStyle = n.color; ctx.globalAlpha = n.alpha; ctx.fill();
        ctx.shadowBlur = 0; ctx.globalAlpha = 1;
        if (n.id === pinned || n.id === focusPin) { const pulse = 1 + 0.18 * Math.sin(now / 380); ctx.beginPath(); ctx.arc(n.x, n.y!, n.r * pulse + 3 / k, 0, 2 * Math.PI); ctx.strokeStyle = "rgba(255,255,255,0.9)"; ctx.lineWidth = 2 / k; ctx.stroke(); }
        else if (n.id === focus) { ctx.beginPath(); ctx.arc(n.x, n.y!, n.r + 2.5 / k, 0, 2 * Math.PI); ctx.strokeStyle = "rgba(255,255,255,0.8)"; ctx.lineWidth = 1.6 / k; ctx.stroke(); }
        if (emphasised || (showLabels && lit && n.r > 4)) labelCands.push({ n, lit, pri: n.id === focus || n.id === pinned ? 3 : emphasised ? 2 : 1 });
      }
      ctx.restore();

      // labels — constant screen size + greedy de-clutter: a label is drawn only
      // if its box clears every higher-priority one already placed. Keeps the
      // radial rim (and any zoomed-in cluster) readable instead of a pile-up.
      ctx.save();
      ctx.scale(dpr, dpr);
      ctx.font = "11px ui-sans-serif, system-ui";
      ctx.textAlign = "center"; ctx.textBaseline = "top";
      labelCands.sort((a, b) => b.pri - a.pri || b.n.r - a.n.r);
      const placed: { x: number; y: number; w: number; h: number }[] = [];
      for (const c of labelCands) {
        const n = c.n;
        const sx = n.x! * k + transform.x, sy = n.y! * k + transform.y + n.r * k + 3;
        if (sx < -80 || sx > width + 80 || sy < -16 || sy > height + 16) continue;
        const w = ctx.measureText(n.label).width;
        const box = { x: sx - w / 2 - 3, y: sy - 2, w: w + 6, h: 15 };
        let clash = false;
        for (const p of placed) { if (box.x < p.x + p.w && box.x + box.w > p.x && box.y < p.y + p.h && box.y + box.h > p.y) { clash = true; break; } }
        if (clash) continue;
        placed.push(box);
        ctx.fillStyle = c.lit ? "rgba(232,232,238,0.94)" : "rgba(160,160,170,0.30)";
        ctx.fillText(n.label, sx, sy);
      }
      ctx.restore();
    }

    // --- animation loop (runs while settling / focused; single-frame otherwise) ---
    let running = false;
    const busy = () => alphaHot || (sim != null && sim.alpha() > sim.alphaMin()) || interactRef.current.hover !== null || interactRef.current.pinned !== null || interactRef.current.focusPin !== null;
    const frame = () => { draw(performance.now()); running = busy(); if (running) requestAnimationFrame(frame); };
    const kick = () => { if (!running) { running = true; requestAnimationFrame(frame); } };
    kickRef.current = kick;

    let sim: d3.Simulation<GNode, GLink> | null = null;
    if (layout === "galaxy") {
      sim = d3.forceSimulation<GNode>(nodes)
        .force("link", d3.forceLink<GNode, GLink>(links).id((d) => d.id).distance(40).strength(0.35))
        .force("charge", d3.forceManyBody().strength(nodes.length > 800 ? -24 : -62))
        .force("center", d3.forceCenter(0, 0)).force("collide", d3.forceCollide<GNode>((d) => d.r + 2.5))
        .force("x", d3.forceX(0).strength(0.02)).force("y", d3.forceY(0).strength(0.02))
        .alpha(1).alphaDecay(0.03);
      sim.on("end", () => setReady(true));
    } else { buildRadial(); setReady(true); }

    function resize() {
      width = wrap!.clientWidth; height = wrap!.clientHeight;
      canvas!.width = width * dpr; canvas!.height = height * dpr; canvas!.style.width = width + "px"; canvas!.style.height = height + "px";
      if (layout === "radial") buildRadial(); kick();
    }
    resize();
    const ro = new ResizeObserver(resize); ro.observe(wrap);

    const sel = d3.select(canvas);
    const zoom = d3.zoom<HTMLCanvasElement, unknown>().scaleExtent([0.05, 6]).on("zoom", (ev) => { transform = ev.transform; kick(); });
    sel.call(zoom);
    const init = d3.zoomIdentity.translate(width / 2, height / 2).scale(layout === "radial" ? 1 : 0.9);
    sel.call(zoom.transform, init); transform = init; kick();

    // yFrac positions the framed group vertically (0.5 = centre). The tour
    // passes a smaller value so the current file clears the bottom HUD.
    focusRef.current = (ids: string[], yFrac = 0.5) => {
      const pts = ids.map((id) => nodeById.get(id)).filter((n): n is GNode => !!n && n.x != null); if (!pts.length) return;
      let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
      for (const n of pts) { minX = Math.min(minX, n.x!); minY = Math.min(minY, n.y!); maxX = Math.max(maxX, n.x!); maxY = Math.max(maxY, n.y!); }
      const bw = Math.max(1, maxX - minX), bh = Math.max(1, maxY - minY);
      const kk = Math.min(3, 0.82 / Math.max(bw / width, bh / height, 0.0001));
      const cx = (minX + maxX) / 2, cy = (minY + maxY) / 2;
      sel.transition().duration(650).call(zoom.transform, d3.zoomIdentity.translate(width / 2 - cx * kk, height * yFrac - cy * kk).scale(kk));
    };
    navRef.current = {
      zoomIn: () => sel.transition().duration(220).call(zoom.scaleBy, 1.45),
      zoomOut: () => sel.transition().duration(220).call(zoom.scaleBy, 0.68),
      fit: () => focusRef.current(nodes.map((n) => n.id)),
      reset: () => sel.transition().duration(400).call(zoom.transform, init),
    };

    const pick = (mx: number, my: number): GNode | null => {
      const x = (mx - transform.x) / transform.k, y = (my - transform.y) / transform.k;
      let best: GNode | null = null, bestD = Infinity;
      for (const n of nodes) { if (n.x == null) continue; const d = (n.x - x) ** 2 + (n.y! - y) ** 2; const rr = (n.r + 4) ** 2; if (d < rr && d < bestD) { bestD = d; best = n; } }
      return best;
    };

    let dragging: GNode | null = null;
    const onMove = (ev: MouseEvent) => {
      const rect = canvas!.getBoundingClientRect(), mx = ev.clientX - rect.left, my = ev.clientY - rect.top;
      if (dragging && layout === "galaxy") { dragging.fx = (mx - transform.x) / transform.k; dragging.fy = (my - transform.y) / transform.k; sim?.alphaTarget(0.2).restart(); kick(); return; }
      const hit = pick(mx, my), id = hit?.id ?? null;
      if (id !== interactRef.current.hover) {
        interactRef.current.hover = id; canvas!.style.cursor = hit ? "pointer" : "grab";
        // Skip the banner for the pinned node — its details are already in the
        // drawer, and the card would otherwise cover its lit-up neighbours.
        if (hit && hit.x != null && hit.id !== interactRef.current.pinned) setHoverInfo({ label: hit.label, folder: hit.folder, color: hit.color, imports: (dir.out.get(hit.id) ?? []).length, importers: (dir.inc.get(hit.id) ?? []).length, sx: hit.x * transform.k + transform.x, sy: hit.y! * transform.k + transform.y });
        else setHoverInfo(null);
        kick();
      }
    };
    const onDown = (ev: MouseEvent) => { if (layout !== "galaxy") return; const rect = canvas!.getBoundingClientRect(); const hit = pick(ev.clientX - rect.left, ev.clientY - rect.top); if (hit) { dragging = hit; ev.stopPropagation(); } };
    const onUp = () => { if (dragging) { dragging.fx = null; dragging.fy = null; sim?.alphaTarget(0); dragging = null; } };
    const onClick = (ev: MouseEvent) => {
      const rect = canvas!.getBoundingClientRect(); const hit = pick(ev.clientX - rect.left, ev.clientY - rect.top);
      if (hit) { setSelected({ id: hit.id, label: hit.label, path: hit.path, kind: hit.kind }); onSelectNode(hit.id, hit.label); setHoverInfo(null); if (hit.path && hit.kind === "file") onExpandFile?.(hit.path); }
      else {
        // Click on empty space = reset: drop the query highlight + selection and
        // return the whole graph to its neutral, full-brightness state.
        interactRef.current.highlight = new Set();
        interactRef.current.focusPin = null;
        setSelected(null); onSelectNode(null, null); onClearHighlight?.(); kick();
      }
    };
    const onDbl = (ev: MouseEvent) => { const rect = canvas!.getBoundingClientRect(); const hit = pick(ev.clientX - rect.left, ev.clientY - rect.top); if (hit) { const n = neighbourMap.get(hit.id); focusRef.current(n ? [hit.id, ...n] : [hit.id]); } };
    canvas.addEventListener("mousemove", onMove); canvas.addEventListener("mousedown", onDown, true);
    window.addEventListener("mouseup", onUp); canvas.addEventListener("click", onClick); canvas.addEventListener("dblclick", onDbl);

    return () => {
      sim?.stop(); ro.disconnect(); sel.on(".zoom", null);
      canvas.removeEventListener("mousemove", onMove); canvas.removeEventListener("mousedown", onDown, true);
      window.removeEventListener("mouseup", onUp); canvas.removeEventListener("click", onClick); canvas.removeEventListener("dblclick", onDbl);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes, links, layout, neighbourMap, dir]);

  useEffect(() => { interactRef.current.pinned = selected?.id ?? null; kickRef.current(); }, [selected]);

  // The guided tour is authored for the galaxy layout (directional flow lives
  // there); force it on and lock the toggle while a tour is active.
  useEffect(() => { if (tourActive) setLayout("galaxy"); }, [tourActive]);

  useEffect(() => {
    // Backend returns files relevance-ordered — keep only the top few so a query
    // spotlights the files it's actually about, not the whole retrieval set.
    const wanted = new Set(highlightedFiles.slice(0, MAX_HIGHLIGHT).map((p) => p.replace(/\\/g, "/")));
    const hl = new Set<string>();
    for (const n of nodes) if (n.path && wanted.has(n.path.replace(/\\/g, "/"))) hl.add(n.id);
    interactRef.current.highlight = hl;
    const ids = [...hl];
    if (ids.length === 1) {
      // A single spotlighted file (tour step / chat focus) becomes the focus
      // node — glow, lit neighbours, directional import/importer flow — and the
      // camera frames its local dependency neighbourhood instead of a lone dot.
      interactRef.current.focusPin = ids[0];
      const nbr = neighbourMap.get(ids[0]);
      // During a tour the bottom HUD covers ~half the canvas — lift the frame.
      focusRef.current(nbr && nbr.size > 0 ? [ids[0], ...nbr] : ids, tourActive ? 0.34 : 0.5);
    } else {
      interactRef.current.focusPin = null;
      if (ids.length > 0) focusRef.current(ids);
    }
    kickRef.current();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [highlightedFiles, focusNonce, nodes]);

  useEffect(() => {
    if (!filterGroup) { interactRef.current.filter = null; kickRef.current(); return; }
    const ids = new Set<string>(); for (const n of nodes) if (n.folder === filterGroup) ids.add(n.id);
    interactRef.current.filter = ids; kickRef.current(); if (ids.size > 0) focusRef.current([...ids]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filterGroup, nodes]);

  useEffect(() => { setFilterGroup(null); setSelected(null); }, [nodes]);

  // keyboard: Esc clears, "/" focuses search
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { setSelected(null); setSearchOpen(false); onSelectNode(null, null); }
      else if (e.key === "/" && document.activeElement !== searchRef.current) { e.preventDefault(); searchRef.current?.focus(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onSelectNode]);

  const navigateTo = useCallback((id: string) => {
    const n = nodeById.get(id); if (!n) return;
    setSelected({ id, label: n.label, path: n.path, kind: n.kind }); onSelectNode(id, n.label);
    if (n.path && n.kind === "file") onExpandFile?.(n.path);
    const nbr = neighbourMap.get(id); focusRef.current(nbr ? [id, ...nbr] : [id]);
    setSearch(""); setSearchOpen(false);
  }, [nodeById, neighbourMap, onSelectNode, onExpandFile]);

  const imports = useMemo(() => (selected ? (dir.out.get(selected.id) ?? []).map((i) => nodeById.get(i)).filter((n): n is GNode => !!n) : []), [selected, dir, nodeById]);
  const importers = useMemo(() => (selected ? (dir.inc.get(selected.id) ?? []).map((i) => nodeById.get(i)).filter((n): n is GNode => !!n) : []), [selected, dir, nodeById]);
  const clearSelection = useCallback(() => { setSelected(null); onSelectNode(null, null); }, [onSelectNode]);

  const loading = graph === null;
  const empty = graph !== null && nodes.length === 0;
  const glass = "border border-white/[0.08] bg-black/50 backdrop-blur-xl shadow-lg shadow-black/40";

  return (
    <div ref={wrapRef} className="relative h-full w-full overflow-hidden bg-[#08080b] [background-image:radial-gradient(120%_120%_at_50%_-10%,rgba(99,102,241,0.10),transparent_55%),radial-gradient(90%_90%_at_50%_110%,rgba(0,0,0,0.6),transparent_60%)]">
      <canvas ref={canvasRef} className="absolute inset-0 block" />

      {loading && (
        <div className="absolute inset-0 z-30 flex flex-col items-center justify-center gap-3">
          <span className="relative flex h-3 w-3"><span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-indigo-400/60" /><span className="relative inline-flex h-3 w-3 rounded-full bg-indigo-400" /></span>
          <span className="text-[13px] text-neutral-400">Building the dependency graph…</span>
        </div>
      )}
      {empty && (
        <div className="absolute inset-0 z-30 flex flex-col items-center justify-center gap-2 text-center">
          <div className="text-[14px] font-medium text-neutral-300">No graph yet</div>
          <div className="max-w-xs text-[12px] text-neutral-500">Select an ingested repository, or ingest one to map its topology.</div>
        </div>
      )}

      {!loading && !empty && (
        <>
          {/* toolbar */}
          <div className="pointer-events-none absolute inset-x-0 top-0 z-10 flex items-start justify-between gap-2 p-3.5">
            <div className="pointer-events-auto flex items-center gap-2">
              <div className={"flex rounded-xl p-0.5 " + glass}>
                {(["galaxy", "radial"] as Layout[]).map((l) => {
                  const locked = tourActive && l === "radial";
                  return (
                    <button key={l} onClick={() => setLayout(l)} disabled={locked}
                      title={locked ? "Locked to Galaxy during the guided tour" : undefined}
                      className={"rounded-lg px-3 py-1.5 text-[11.5px] font-medium capitalize transition-colors " + (layout === l ? "bg-white/[0.09] text-neutral-50 shadow-sm" : locked ? "cursor-not-allowed text-neutral-700" : "text-neutral-500 hover:text-neutral-300")}>
                      {l === "galaxy" ? "◍ Galaxy" : "❋ Radial"}
                    </button>
                  );
                })}
              </div>
              <div className="relative">
                <input ref={searchRef} value={search} onChange={(e) => { setSearch(e.target.value); setSearchOpen(true); }} onFocus={() => setSearchOpen(true)} onBlur={() => setTimeout(() => setSearchOpen(false), 150)}
                  placeholder="Search files…  ( / )" className={"w-52 rounded-xl px-3 py-2 text-[11.5px] text-neutral-100 placeholder:text-neutral-600 focus:border-indigo-400/40 focus:outline-none " + glass} />
                {searchOpen && searchResults.length > 0 && (
                  <div className={"absolute left-0 top-full z-20 mt-1.5 w-72 overflow-hidden rounded-xl " + glass}>
                    {searchResults.map((n) => (
                      <button key={n.id} onMouseDown={() => navigateTo(n.id)} className="flex w-full items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-white/[0.05]">
                        <span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: n.color }} />
                        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-neutral-200">{n.label}</span>
                        <span className="shrink-0 truncate text-[9px] text-neutral-600">{n.folder}</span>
                      </button>
                    ))}
                  </div>
                )}
              </div>
            </div>
            <div className={"pointer-events-auto rounded-xl px-3 py-2 text-[10px] text-neutral-500 " + glass}>
              <span className="text-neutral-300">{nodes.length.toLocaleString()}</span> nodes · <span className="text-neutral-300">{links.length.toLocaleString()}</span> links{!ready && layout === "galaxy" && <span className="ml-1 text-indigo-300">· settling…</span>}
            </div>
          </div>

          {/* nav controls */}
          <div className={"absolute bottom-3.5 z-10 flex flex-col gap-1 transition-[right] " + (selected ? "right-[21.5rem]" : "right-3.5")}>
            {([["+", () => navRef.current?.zoomIn(), "Zoom in"], ["−", () => navRef.current?.zoomOut(), "Zoom out"], ["⊡", () => navRef.current?.fit(), "Fit to screen"], ["⟲", () => navRef.current?.reset(), "Reset view"]] as const).map(([g, fn, t]) => (
              <button key={t} onClick={fn} title={t} className={"flex h-8 w-8 items-center justify-center rounded-xl text-[14px] text-neutral-300 transition-colors hover:text-indigo-200 " + glass}>{g}</button>
            ))}
          </div>

          {/* hover info card (anchored to the node) */}
          {hoverInfo && (
            <div className={"pointer-events-none absolute z-20 -translate-x-1/2 rounded-xl px-3 py-2 " + glass} style={{ left: Math.max(90, Math.min((wrapRef.current?.clientWidth ?? 9999) - 90, hoverInfo.sx)), top: Math.max(60, hoverInfo.sy - 62) }}>
              <div className="max-w-[220px] truncate font-mono text-[11.5px] font-medium text-neutral-100">{hoverInfo.label}</div>
              <div className="mt-1 flex items-center gap-2 text-[10px]">
                <span className="flex items-center gap-1 text-neutral-400"><span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: hoverInfo.color }} />{hoverInfo.folder}</span>
                <span className="text-neutral-600">·</span>
                <span style={{ color: OUT_COLOR }}>↓{hoverInfo.imports}</span>
                <span style={{ color: IN_COLOR }}>↑{hoverInfo.importers}</span>
              </div>
            </div>
          )}

          {/* legend — click a swatch to isolate that module */}
          {folders.length > 0 && perspective !== "executive" && (
            <div className={"absolute bottom-3.5 left-3.5 z-10 flex max-w-[44%] flex-wrap items-center gap-x-2.5 gap-y-1 rounded-xl px-3 py-2 " + glass}>
              {folders.slice(0, 10).map((f, i) => {
                const active = filterGroup === f;
                return (
                  <button key={f} onClick={() => setFilterGroup(active ? null : f)} title={active ? "Show all" : `Isolate ${f}`} className={"flex items-center gap-1.5 rounded-md px-1 py-0.5 text-[10px] transition-colors " + (active ? "bg-white/[0.09] text-neutral-100" : "text-neutral-400 hover:text-neutral-200") + (filterGroup && !active ? " opacity-40" : "")}>
                    <span className="h-2 w-2 rounded-full" style={{ backgroundColor: PALETTE[i % PALETTE.length] }} />{f}
                  </button>
                );
              })}
              {filterGroup && <button onClick={() => setFilterGroup(null)} className="rounded-md px-1.5 py-0.5 text-[10px] text-indigo-300 hover:text-indigo-200">✕ clear</button>}
            </div>
          )}

          {/* detail drawer */}
          {selected && (
            <aside className="absolute right-0 top-0 z-20 flex h-full w-[21rem] flex-col border-l border-white/[0.08] bg-[#0b0b0e]/95 shadow-2xl shadow-black/60 backdrop-blur-xl">
              <div className="border-b border-white/[0.07] bg-gradient-to-b from-white/[0.03] to-transparent px-4 py-3.5">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="truncate text-[13.5px] font-semibold text-neutral-50">{selected.label}</div>
                    {selected.path && <div className="mt-0.5 truncate font-mono text-[10px] text-neutral-500">{selected.path}</div>}
                  </div>
                  <button onClick={clearSelection} className="shrink-0 rounded-md px-1 text-neutral-500 transition-colors hover:bg-white/5 hover:text-neutral-200">✕</button>
                </div>
              </div>
              <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-3.5 py-3.5">
                {selected.path && (
                  <FileSummary
                    text={fileSummary?.path === selected.path ? fileSummary.text : ""}
                    loading={summaryLoading && fileSummary?.path !== selected.path}
                  />
                )}
                <Connections title="Imports" hint="depends on" color={OUT_COLOR} items={imports} onGo={navigateTo} />
                <Connections title="Imported by" hint="depended on by" color={IN_COLOR} items={importers} onGo={navigateTo} />
                {!selected.path && <div className="px-1 text-[12px] text-neutral-500">External dependency / route — no local source to open.</div>}
              </div>
            </aside>
          )}
        </>
      )}
    </div>
  );
}

function FileSummary({ text, loading }: { text: string; loading: boolean }) {
  if (!text && !loading) return null;
  return (
    <div className="rounded-lg border border-white/[0.07] bg-white/[0.025] px-3 py-2.5">
      <div className="mb-1.5 flex items-center gap-1.5">
        <span className="text-[10px] font-medium uppercase tracking-wider text-neutral-500">Summary</span>
        <span className="rounded bg-indigo-500/[0.12] px-1 py-px text-[8px] font-semibold uppercase tracking-wider text-indigo-300/90">AI</span>
      </div>
      {loading ? (
        <div className="space-y-1.5 py-0.5">
          <div className="h-2 w-full animate-pulse rounded bg-white/[0.06]" />
          <div className="h-2 w-[86%] animate-pulse rounded bg-white/[0.06]" />
          <div className="h-2 w-[62%] animate-pulse rounded bg-white/[0.06]" />
        </div>
      ) : (
        <p className="text-[12px] leading-relaxed text-neutral-300">{text}</p>
      )}
    </div>
  );
}

function Connections({ title, hint, color, items, onGo }: { title: string; hint: string; color: string; items: GNode[]; onGo: (id: string) => void }) {
  if (items.length === 0) return null;
  return (
    <div>
      <div className="mb-1.5 flex items-center gap-1.5 px-1">
        <span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: color }} />
        <span className="text-[10px] font-medium uppercase tracking-wider text-neutral-400">{title}</span>
        <span className="text-[10px] text-neutral-600">· {items.length} {hint}</span>
      </div>
      <div className="flex flex-wrap gap-1">
        {items.slice(0, 40).map((n) => (
          <button key={n.id} onClick={() => onGo(n.id)} title={n.path ?? n.label} className="max-w-full truncate rounded-md border border-white/[0.08] bg-white/[0.03] px-1.5 py-0.5 font-mono text-[10px] text-neutral-300 transition-colors hover:border-indigo-400/40 hover:bg-indigo-400/[0.08] hover:text-indigo-200">{n.label}</button>
        ))}
        {items.length > 40 && <span className="px-1 text-[10px] text-neutral-600">+{items.length - 40}</span>}
      </div>
    </div>
  );
}

