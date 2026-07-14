"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { gsap } from "gsap";

import {
  downloadJSON,
  exportFilename,
  fetchBugs,
  fetchPrune,
  type Bug,
  type BugReport,
  type PruneCandidate,
  type PruneReport,
} from "../workspace/lib/api";

// --- refined design tokens --------------------------------------------------
const SEV: Record<string, { label: string; color: string; soft: string }> = {
  CRITICAL: { label: "Critical", color: "#f87171", soft: "rgba(248,113,113,0.12)" },
  HIGH: { label: "High", color: "#fb923c", soft: "rgba(251,146,60,0.12)" },
  MEDIUM: { label: "Medium", color: "#fbbf24", soft: "rgba(251,191,36,0.12)" },
  LOW: { label: "Low", color: "#94a3b8", soft: "rgba(148,163,184,0.12)" },
};
const sev = (s: string) => SEV[s] ?? SEV.LOW;

const CATEGORY: Record<string, string> = {
  circular_dependency: "Circular dependency",
  resource_leak: "Resource leak",
  bad_practice: "Bad practice",
  security: "Security",
  logic: "Logic",
  concurrency: "Concurrency",
  error_handling: "Error handling",
};
const catLabel = (c: string) => CATEGORY[c] ?? c.replace(/_/g, " ").replace(/\b\w/g, (m) => m.toUpperCase());

const PRUNE_TIER: Record<string, { label: string; color: string }> = {
  orphan_file: { label: "Orphan file", color: "#fb7185" },
  dead_cluster: { label: "Dead cluster", color: "#fb923c" },
  unused_export: { label: "Unused export", color: "#fbbf24" },
  unused_function: { label: "Unused function", color: "#94a3b8" },
};

type Tab = "issues" | "dead";

export default function PruneDiagnostics() {
  const router = useRouter();
  const [repo, setRepo] = useState<string | null>(null);
  const [bugs, setBugs] = useState<BugReport | null>(null);
  const [prune, setPrune] = useState<PruneReport | null>(null);
  const [bugState, setBugState] = useState<"loading" | "ready" | "error">("loading");
  const [pruneState, setPruneState] = useState<"loading" | "ready" | "error">("loading");
  const [tab, setTab] = useState<Tab>("issues");
  const [refreshing, setRefreshing] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setRepo(new URLSearchParams(window.location.search).get("repo"));
  }, []);

  useEffect(() => {
    if (repo === null) return;
    const ctrl = new AbortController();
    setBugState("loading");
    setPruneState("loading");
    fetchBugs(repo, ctrl.signal)
      .then((b) => (setBugs(b), setBugState("ready")))
      .catch((e) => e?.name !== "AbortError" && setBugState("error"));
    fetchPrune(repo, ctrl.signal)
      .then((p) => (setPrune(p), setPruneState("ready")))
      .catch((e) => e?.name !== "AbortError" && setPruneState("error"));
    return () => ctrl.abort();
  }, [repo]);

  async function rescan() {
    if (!repo || refreshing) return;
    setRefreshing(true);
    setBugState("loading");
    setPruneState("loading");
    const enc = encodeURIComponent(repo);
    const base = (path: string) =>
      fetch(`${process.env.NEXT_PUBLIC_SYNAPSE_API ?? "http://localhost:8080"}/api/${path}?repo=${enc}&refresh=true`).then((r) => r.json());
    try {
      const [b, p] = await Promise.allSettled([base("bugs"), base("prune")]);
      if (b.status === "fulfilled") { setBugs(b.value); setBugState("ready"); } else setBugState("error");
      if (p.status === "fulfilled") { setPrune(p.value); setPruneState("ready"); } else setPruneState("error");
    } finally {
      setRefreshing(false);
    }
  }

  // Subtle entrance + per-tab reveal.
  useEffect(() => {
    if (!rootRef.current) return;
    const ctx = gsap.context(() => {
      gsap.fromTo(".diag-fade", { opacity: 0, y: 10 }, { opacity: 1, y: 0, duration: 0.5, stagger: 0.06, ease: "power2.out" });
    }, rootRef);
    return () => ctx.revert();
  }, []);
  useEffect(() => {
    if (!rootRef.current) return;
    gsap.fromTo(".diag-item", { opacity: 0, y: 8 }, { opacity: 1, y: 0, duration: 0.35, stagger: 0.035, ease: "power2.out" });
  }, [tab, bugState, pruneState]);

  const repoQuery = repo ? `?repo=${encodeURIComponent(repo)}` : "";
  const name = bugs?.name ?? prune?.name ?? "—";

  const counts = useMemo(() => {
    const c = { CRITICAL: 0, HIGH: 0, MEDIUM: 0, LOW: 0 };
    for (const b of bugs?.bugs ?? []) if (b.severity in c) c[b.severity as keyof typeof c]++;
    return c;
  }, [bugs]);
  const bugCount = bugs?.bugs.length ?? 0;
  const deadCount = prune?.candidates.length ?? 0;
  const scanning = bugState === "loading" || pruneState === "loading";
  const canExport = !scanning && (bugs !== null || prune !== null);

  // One file carrying both reports — bugs + anti-patterns and dead code — with
  // the counts that produced them, so the export stands alone outside the UI.
  function exportReports() {
    if (!canExport) return;
    downloadJSON(exportFilename(name, "diagnostics"), {
      schema: "synapse.diagnostics/v1",
      repo,
      name,
      generated_at: new Date().toISOString(),
      summary: {
        bugs_total: bugCount,
        bugs_by_severity: counts,
        dead_code_total: deadCount,
        dead_code_by_tier: prune?.summary ?? {},
        files_scanned: bugs?.scanned ?? prune?.code_files ?? 0,
      },
      bugs: bugs ?? null,
      dead_code: prune ?? null,
    });
  }

  return (
    <div ref={rootRef} className="relative h-full overflow-y-auto bg-[#0a0a0c] text-neutral-300">
      {/* ambient glow */}
      <div className="pointer-events-none absolute inset-x-0 top-0 h-[380px] bg-[radial-gradient(60%_100%_at_50%_0%,rgba(99,102,241,0.10),transparent_70%)]" />

      {/* nav */}
      <header className="sticky top-0 z-20 flex items-center justify-between border-b border-white/[0.06] bg-[#0a0a0c]/80 px-6 py-3 backdrop-blur-xl">
        <button onClick={() => router.push("/")} className="flex items-center gap-2.5">
          <span className="h-2 w-2 rounded-full bg-indigo-400 shadow-[0_0_10px_1px_rgba(129,140,248,0.7)]" />
          <span className="text-[13px] font-semibold tracking-tight text-neutral-100">Synapse</span>
          <span className="text-neutral-600">/</span>
          <span className="text-[13px] text-neutral-400">Diagnostics</span>
        </button>
        <div className="flex items-center gap-1.5 text-[12px]">
          {([["Docs", `/docs${repoQuery}`], ["Architecture", `/architecture${repoQuery}`], ["Workspace", `/workspace${repoQuery}`]] as const).map(
            ([label, href]) => (
              <button
                key={label}
                onClick={() => router.push(href)}
                className="rounded-lg px-3 py-1.5 text-neutral-400 transition-colors hover:bg-white/[0.05] hover:text-neutral-100"
              >
                {label}
              </button>
            ),
          )}
          <button
            onClick={exportReports}
            disabled={!canExport}
            title="Download the bug, anti-pattern, and dead-code reports as JSON"
            className="ml-1 flex items-center gap-1.5 rounded-lg border border-white/[0.08] bg-white/[0.03] px-3 py-1.5 text-neutral-300 transition-colors hover:border-emerald-400/40 hover:text-emerald-200 disabled:cursor-not-allowed disabled:opacity-40"
          >
            <span aria-hidden>↓</span> Export JSON
          </button>
          <button
            onClick={rescan}
            disabled={scanning}
            className="flex items-center gap-1.5 rounded-lg border border-white/[0.08] bg-white/[0.03] px-3 py-1.5 text-neutral-300 transition-colors hover:border-indigo-400/40 hover:text-indigo-200 disabled:opacity-50"
          >
            <span className={refreshing ? "animate-spin" : ""}>↻</span> Rescan
          </button>
        </div>
      </header>

      <div className="relative z-10 mx-auto max-w-5xl px-6 py-10">
        {/* hero */}
        <div className="diag-fade">
          <div className="text-[11px] font-medium uppercase tracking-[0.2em] text-indigo-300/70">Codebase diagnostics</div>
          <h1 className="mt-2 text-[30px] font-semibold tracking-tight text-neutral-50">
            {name}
          </h1>
          <p className="mt-1.5 max-w-xl text-[13.5px] leading-relaxed text-neutral-500">
            Bugs, anti-patterns, and dead code — heuristic candidates confirmed against their real source by an
            AI verification pass to keep false positives out.
          </p>
        </div>

        {/* summary */}
        <div className="diag-fade mt-7 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
          <Stat label="Critical" value={counts.CRITICAL} color={SEV.CRITICAL.color} loading={bugState === "loading"} />
          <Stat label="High" value={counts.HIGH} color={SEV.HIGH.color} loading={bugState === "loading"} />
          <Stat label="Medium" value={counts.MEDIUM} color={SEV.MEDIUM.color} loading={bugState === "loading"} />
          <Stat label="Dead code" value={deadCount} color="#fb923c" loading={pruneState === "loading"} />
          <Stat label="Files" value={bugs?.scanned ?? prune?.code_files ?? 0} color="#818cf8" loading={scanning} />
        </div>

        {/* segmented tabs */}
        <div className="diag-fade mt-8 inline-flex rounded-xl border border-white/[0.07] bg-white/[0.02] p-1">
          <TabBtn active={tab === "issues"} onClick={() => setTab("issues")} label="Bugs & anti-patterns" count={bugCount} state={bugState} />
          <TabBtn active={tab === "dead"} onClick={() => setTab("dead")} label="Dead code" count={deadCount} state={pruneState} />
        </div>

        {/* content */}
        <div className="mt-5">
          {tab === "issues" ? (
            <Panel
              state={bugState}
              loadingText="Running deterministic scans, AI verification, and adversarial review…"
              emptyText="No bugs or anti-patterns detected — clean scan."
              empty={(bugs?.bugs.length ?? 0) === 0}
            >
              <div className="space-y-2.5">
                {(bugs?.bugs ?? []).map((b) => (
                  <BugCard key={b.bug_id} bug={b} />
                ))}
              </div>
            </Panel>
          ) : (
            <Panel
              state={pruneState}
              loadingText="Tracing reachability and verifying candidates against framework conventions…"
              emptyText="No orphaned files or unused symbols — the graph is fully reachable."
              empty={deadCount === 0}
            >
              <div className="space-y-1.5">
                {(prune?.candidates ?? []).map((c, i) => (
                  <DeadRow key={c.path + c.symbol + i} c={c} />
                ))}
              </div>
            </Panel>
          )}
        </div>

        {/* caveats */}
        {(bugs?.notes?.length || prune?.notes?.length) && (
          <div className="diag-fade mt-8 rounded-xl border border-white/[0.06] bg-white/[0.015] px-4 py-3 text-[11.5px] leading-relaxed text-neutral-500">
            {[...(bugs?.notes ?? []), ...(prune?.notes ?? [])].map((n, i) => (
              <div key={i} className="flex gap-2">
                <span className="mt-[3px] text-neutral-700">·</span>
                <span>{n}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function Stat({ label, value, color, loading }: { label: string; value: number; color: string; loading: boolean }) {
  return (
    <div className="rounded-xl border border-white/[0.06] bg-white/[0.02] px-3.5 py-3">
      <div className="text-[10px] font-medium uppercase tracking-wider text-neutral-500">{label}</div>
      <div className="mt-1.5 text-[24px] font-semibold leading-none tabular-nums" style={{ color: loading ? "#3f3f46" : color }}>
        {loading ? "—" : value}
      </div>
    </div>
  );
}

function TabBtn({ active, onClick, label, count, state }: { active: boolean; onClick: () => void; label: string; count: number; state: string }) {
  return (
    <button
      onClick={onClick}
      className={
        "flex items-center gap-2 rounded-lg px-4 py-2 text-[12.5px] font-medium transition-colors " +
        (active ? "bg-white/[0.07] text-neutral-100 shadow-sm" : "text-neutral-500 hover:text-neutral-300")
      }
    >
      {label}
      <span className={"rounded-full px-1.5 py-0.5 text-[10px] tabular-nums " + (active ? "bg-indigo-400/15 text-indigo-300" : "bg-white/[0.05] text-neutral-500")}>
        {state === "loading" ? "…" : count}
      </span>
    </button>
  );
}

function Panel({ state, loadingText, emptyText, empty, children }: {
  state: string; loadingText: string; emptyText: string; empty: boolean; children: React.ReactNode;
}) {
  if (state === "loading")
    return (
      <div className="flex items-center gap-3 rounded-xl border border-white/[0.06] bg-white/[0.02] px-5 py-8">
        <span className="relative flex h-2.5 w-2.5">
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-indigo-400/60" />
          <span className="relative inline-flex h-2.5 w-2.5 rounded-full bg-indigo-400" />
        </span>
        <span className="text-[13px] text-neutral-400">{loadingText}</span>
      </div>
    );
  if (state === "error")
    return <div className="rounded-xl border border-rose-500/20 bg-rose-500/[0.05] px-5 py-6 text-[13px] text-rose-300">Analysis unavailable — is the backend running on :8080?</div>;
  if (empty)
    return (
      <div className="flex items-center gap-3 rounded-xl border border-emerald-500/20 bg-emerald-500/[0.04] px-5 py-6 text-[13px] text-emerald-300/90">
        <span className="text-emerald-400">✓</span> {emptyText}
      </div>
    );
  return <>{children}</>;
}

function Chip({ children, color, soft }: { children: React.ReactNode; color?: string; soft?: string }) {
  return (
    <span
      className="rounded-md px-1.5 py-0.5 text-[10px] font-medium"
      style={color ? { color, backgroundColor: soft ?? "transparent" } : { color: "#a1a1aa", backgroundColor: "rgba(255,255,255,0.04)" }}
    >
      {children}
    </span>
  );
}

function BugCard({ bug }: { bug: Bug }) {
  const [open, setOpen] = useState(bug.severity === "CRITICAL");
  const s = sev(bug.severity);
  const loc = bug.location;
  const lines = loc.line_start ? `:${loc.line_start}${loc.line_end && loc.line_end !== loc.line_start ? `-${loc.line_end}` : ""}` : "";
  const verified = bug.tier === "verified";
  const isAI = bug.tier === "llm";
  return (
    <div
      className="diag-item overflow-hidden rounded-xl border border-white/[0.07] bg-white/[0.02] transition-colors hover:bg-white/[0.03]"
      style={{ boxShadow: `inset 3px 0 0 ${s.color}` }}
    >
      <button onClick={() => setOpen((o) => !o)} className="flex w-full items-start gap-3 px-4 py-3 text-left">
        <span className="mt-1 h-1.5 w-1.5 shrink-0 rounded-full" style={{ backgroundColor: s.color, boxShadow: `0 0 8px ${s.color}` }} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-[10px] font-semibold uppercase tracking-wide" style={{ color: s.color }}>{s.label}</span>
            <span className="text-neutral-700">·</span>
            <Chip>{catLabel(bug.category)}</Chip>
            {verified && <Chip color="#34d399" soft="rgba(52,211,153,0.1)">✓ Verified</Chip>}
            {isAI && <Chip color="#c4b5fd" soft="rgba(196,181,253,0.1)">AI analysis</Chip>}
            <Confidence level={bug.confidence} />
          </div>
          <div className="mt-1.5 text-[14px] font-medium leading-snug text-neutral-100">{bug.title}</div>
          <div className="mt-1 truncate font-mono text-[11px] text-neutral-500">
            {loc.file}<span className="text-neutral-600">{lines}</span>
            {loc.entity && <span className="text-neutral-600"> · {loc.entity}</span>}
          </div>
        </div>
        <span className="mt-1 shrink-0 select-none text-[11px] text-neutral-600">{open ? "▾" : "▸"}</span>
      </button>
      {open && (
        <div className="space-y-3 border-t border-white/[0.06] px-4 py-3.5 pl-[42px] text-[12.5px] leading-relaxed">
          <Field label="Issue" color={s.color} text={bug.finding.issue} />
          <Field label="Impact" color="#fb923c" text={bug.finding.impact} />
          <Field label="Fix" color="#34d399" text={bug.finding.fix} />
          {bug.context_nodes.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
              <span className="text-[10px] uppercase tracking-wider text-neutral-600">Related</span>
              {bug.context_nodes.slice(0, 8).map((n) => (
                <code key={n} className="rounded bg-white/[0.04] px-1.5 py-0.5 font-mono text-[10px] text-neutral-400">{n.split("/").pop()}</code>
              ))}
              {bug.context_nodes.length > 8 && <span className="text-[10px] text-neutral-600">+{bug.context_nodes.length - 8}</span>}
            </div>
          )}
          <div className="pt-1 font-mono text-[10px] text-neutral-700">{bug.bug_id}</div>
        </div>
      )}
    </div>
  );
}

function Field({ label, color, text }: { label: string; color: string; text: string }) {
  if (!text) return null;
  return (
    <div>
      <div className="mb-0.5 text-[10px] font-semibold uppercase tracking-wider" style={{ color }}>{label}</div>
      <p className="text-neutral-300">{text}</p>
    </div>
  );
}

function Confidence({ level }: { level: string }) {
  const n = level === "high" ? 3 : level === "medium" ? 2 : 1;
  return (
    <span className="ml-1 inline-flex items-center gap-1" title={`${level || "low"} confidence`}>
      <span className="flex items-center gap-0.5">
        {[0, 1, 2].map((i) => (
          <span key={i} className="h-2.5 w-[3px] rounded-full" style={{ backgroundColor: i < n ? "#818cf8" : "rgba(255,255,255,0.12)" }} />
        ))}
      </span>
      <span className="text-[10px] text-neutral-500">{level || "low"}</span>
    </span>
  );
}

function DeadRow({ c }: { c: PruneCandidate }) {
  const [open, setOpen] = useState(false);
  const meta = PRUNE_TIER[c.tier] ?? { label: c.tier, color: "#94a3b8" };
  const hasDetail = !!c.reason || c.evidence.length > 0;
  return (
    <div className="diag-item overflow-hidden rounded-lg border border-white/[0.06] bg-white/[0.02]">
      <button onClick={() => hasDetail && setOpen((o) => !o)} className="flex w-full items-center gap-2.5 px-3.5 py-2.5 text-left">
        <span className="h-1.5 w-1.5 shrink-0 rounded-full" style={{ backgroundColor: meta.color }} />
        <span className="shrink-0 text-[10px] font-medium uppercase tracking-wide" style={{ color: meta.color }}>{meta.label}</span>
        <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-neutral-200">
          {c.path}
          {c.symbol && <span className="text-indigo-300/80"> · {c.symbol}</span>}
        </span>
        {c.uncertain && <Chip color="#fbbf24" soft="rgba(251,191,36,0.1)">review</Chip>}
        <span className="shrink-0 text-[10px] text-neutral-600">{c.confidence}</span>
        {hasDetail && <span className="shrink-0 select-none text-[10px] text-neutral-600">{open ? "▾" : "▸"}</span>}
      </button>
      {open && hasDetail && (
        <div className="border-t border-white/[0.06] px-3.5 py-2.5 pl-[34px] text-[12px] text-neutral-400">
          {c.reason && <p className="text-neutral-300">{c.reason}</p>}
          {c.evidence.length > 0 && (
            <ul className="mt-1.5 space-y-0.5">
              {c.evidence.map((e, i) => (
                <li key={i} className="flex gap-2 text-[11.5px] text-neutral-500">
                  <span className="text-neutral-700">–</span> {e}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
