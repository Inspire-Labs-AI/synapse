"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { signIn, signOut, useSession } from "next-auth/react";
import { gsap } from "gsap";

import {
  startIngest,
  fetchIngestStatus,
  fetchRepos,
  deleteRepo,
  type IngestStatus,
  type RepoInfo,
} from "../workspace/lib/api";

// High-contrast split-reveal headline. Highlighted words pop in accent/neon.
const HEADLINE: { w: string; hl?: "accent" | "neon" | "white" }[] = [
  { w: "Map" },
  { w: "Codebases.", hl: "neon" },
  { w: "Expose" },
  { w: "Capabilities.", hl: "accent" },
  { w: "Synapse" },
  { w: "Knowledge.", hl: "white" },
];

type Tab = "local" | "repo";
type Phase = "idle" | "running" | "error";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function stagesFor(tab: Tab): string[] {
  return [
    "Establishing Auth Handshake...",
    tab === "local" ? "Linking Local Directory..." : "Shallow Cloning Target Repository...",
    "Extracting Absolute Abstract Syntax Trees (AST)...",
    "Generating 1024-d Semantic Vector Matrices...",
  ];
}

export default function Onboarding({
  githubEnabled,
  googleEnabled,
}: {
  githubEnabled: boolean;
  googleEnabled: boolean;
}) {
  const router = useRouter();
  const { data: session, status } = useSession();
  const authed = status === "authenticated";

  const rootRef = useRef<HTMLDivElement>(null);
  const heroRef = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<HTMLDivElement>(null);
  const pollRef = useRef<{ cancelled: boolean }>({ cancelled: false });

  const [tab, setTab] = useState<Tab>("local");
  const [localPath, setLocalPath] = useState("");
  const [repoUrl, setRepoUrl] = useState("");
  const [pat, setPat] = useState("");
  const [showPat, setShowPat] = useState(false);
  const [phase, setPhase] = useState<Phase>("idle");
  const [error, setError] = useState<string | null>(null);
  const [progress, setProgress] = useState<IngestStatus | null>(null);

  // --- Mount: cinematic hero + gateway reveal -------------------------------
  useEffect(() => {
    const ctx = gsap.context(() => {
      const tl = gsap.timeline();
      tl.from(".brand-mark", { opacity: 0, y: -10, duration: 0.5, ease: "power2.out" })
        .from(
          ".reveal-word",
          { opacity: 0, yPercent: 120, filter: "blur(10px)", duration: 0.75, ease: "power4.out", stagger: 0.06 },
          "-=0.1",
        )
        .from(".hero-sub", { opacity: 0, y: 12, duration: 0.6, ease: "power2.out" }, "-=0.3")
        .from(".gateway", { opacity: 0, y: 20, scale: 0.98, duration: 0.6, ease: "power3.out" }, "-=0.35");
    }, rootRef);
    return () => ctx.revert();
  }, []);

  // --- Slide the Cortex panel open once authenticated -----------------------
  useEffect(() => {
    if (authed && phase === "idle" && panelRef.current) {
      gsap.fromTo(
        panelRef.current,
        { opacity: 0, height: 0, y: 8 },
        { opacity: 1, height: "auto", y: 0, duration: 0.55, ease: "power3.out" },
      );
    }
  }, [authed, phase]);

  // --- Morph into the terminal matrix when a run starts ---------------------
  useEffect(() => {
    if (phase !== "running" || !termRef.current) return;
    const ctx = gsap.context(() => {
      gsap.fromTo(termRef.current, { opacity: 0, y: 14 }, { opacity: 1, y: 0, duration: 0.45, ease: "power2.out" });
      gsap.fromTo(
        ".term-line",
        { opacity: 0, x: -8 },
        { opacity: 1, x: 0, duration: 0.5, ease: "power2.out", stagger: 0.55 },
      );
    }, termRef);
    return () => ctx.revert();
  }, [phase]);

  useEffect(
    () => () => {
      pollRef.current.cancelled = true;
    },
    [],
  );

  // --- Cinematic exit to the workspace docs ---------------------------------
  const exitTo = useCallback(
    (href: string) => {
      if (!rootRef.current) {
        router.push(href);
        return;
      }
      gsap.to(rootRef.current, {
        scale: 1.06,
        opacity: 0,
        filter: "blur(8px)",
        duration: 0.7,
        ease: "power2.in",
        onComplete: () => router.push(href),
      });
    },
    [router],
  );

  const pollStatus = useCallback(
    async (jobId: string) => {
      const token = pollRef.current;
      while (!token.cancelled) {
        let s: IngestStatus;
        try {
          s = await fetchIngestStatus(jobId);
        } catch {
          await sleep(1500);
          continue;
        }
        if (token.cancelled) return;
        setProgress(s);
        if (s.status === "done") {
          exitTo(`/docs?repo=${encodeURIComponent(s.root_path)}`);
          return;
        }
        if (s.status === "error") {
          setError(s.error ?? "ingestion failed");
          setPhase("error");
          return;
        }
        await sleep(1400);
      }
    },
    [exitTo],
  );

  const launch = useCallback(async () => {
    const input =
      tab === "local" ? { localPath: localPath.trim() } : { repoUrl: repoUrl.trim(), pat: pat.trim() };
    if (tab === "local" && !input.localPath) return;
    if (tab === "repo" && !input.repoUrl) return;

    setError(null);
    setProgress(null);
    setPhase("running");
    pollRef.current = { cancelled: false };
    try {
      const job = await startIngest(input);
      void pollStatus(job.job_id);
    } catch (err) {
      setError(err instanceof Error ? err.message : "could not start ingestion");
      setPhase("error");
    }
  }, [tab, localPath, repoUrl, pat, pollStatus]);

  // Open an already-ingested repo straight in the workspace (cinematic exit).
  const openRepo = useCallback(
    (root: string) => exitTo(`/workspace?repo=${encodeURIComponent(root)}`),
    [exitTo],
  );

  const canLaunch = tab === "local" ? localPath.trim().length > 0 : repoUrl.trim().length > 0;
  const st = progress?.status ?? "queued";
  const discovered = progress?.files_discovered ?? 0;
  const done = progress?.files_done ?? 0;
  const pct = discovered > 0 ? Math.min(100, Math.round((done / discovered) * 100)) : 0;

  return (
    <div ref={rootRef} className="relative h-full w-full overflow-y-auto bg-black">
      {/* Ambient grid + radial glow */}
      <div
        className="pointer-events-none fixed inset-0 opacity-[0.18]"
        style={{
          backgroundImage:
            "linear-gradient(rgba(255,255,255,0.06) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,0.06) 1px, transparent 1px)",
          backgroundSize: "44px 44px",
          maskImage: "radial-gradient(ellipse 70% 60% at 50% 35%, #000 40%, transparent 100%)",
        }}
      />
      <div
        className="pointer-events-none fixed inset-x-0 top-[-20%] h-[60%]"
        style={{ background: "radial-gradient(ellipse 50% 100% at 50% 0%, rgba(99,102,241,0.16), transparent 70%)" }}
      />

      <div className="relative z-10 mx-auto flex min-h-full max-w-2xl flex-col items-center justify-center px-6 py-16">
        {/* Brand */}
        <div className="brand-mark mb-9 flex items-center gap-2.5">
          <span className="inline-block h-2.5 w-2.5 rounded-full bg-neon shadow-[0_0_14px_2px_var(--color-neon)]" />
          <span className="font-mono text-sm font-semibold tracking-tight text-neutral-200">
            project<span className="text-accent">·</span>synapse
          </span>
        </div>

        {/* Hero headline */}
        <div ref={heroRef} className="text-center">
          <h1 className="font-mono text-[34px] font-bold leading-[1.15] tracking-tight sm:text-5xl">
            {HEADLINE.map((seg, i) => (
              <span key={i} className="inline-block overflow-hidden align-bottom">
                <span
                  className={
                    "reveal-word mr-3 inline-block " +
                    (seg.hl === "neon"
                      ? "text-neon"
                      : seg.hl === "accent"
                        ? "text-accent"
                        : seg.hl === "white"
                          ? "text-white"
                          : "text-neutral-400")
                  }
                >
                  {seg.w}
                </span>
              </span>
            ))}
          </h1>
          <p className="hero-sub mx-auto mt-5 max-w-md text-sm leading-relaxed text-neutral-500">
            Ingest any repository or local project to build its{" "}
            <span className="text-neutral-300">AST topology</span>, a{" "}
            <span className="text-neutral-300">semantic vector index</span>, and reusable capability blueprints.
          </p>
        </div>

        {/* Gateway / Onboarding controller */}
        <div className="gateway mt-12 w-full">
          <div className="rounded-2xl border border-panel-border bg-panel/70 p-1.5 shadow-2xl backdrop-blur-xl">
            <div className="rounded-xl border border-white/5 bg-black/40 p-6">
              {status === "loading" ? (
                <div className="flex items-center justify-center gap-1.5 py-8">
                  <span className="dot-pulse h-2 w-2 rounded-full bg-neon" />
                  <span className="dot-pulse h-2 w-2 rounded-full bg-neon [animation-delay:0.15s]" />
                  <span className="dot-pulse h-2 w-2 rounded-full bg-neon [animation-delay:0.3s]" />
                </div>
              ) : !authed ? (
                <SignInView githubEnabled={githubEnabled} googleEnabled={googleEnabled} />
              ) : phase === "running" ? (
                <Terminal
                  termRef={termRef}
                  stages={stagesFor(tab)}
                  source={tab === "local" ? localPath : repoUrl}
                  status={st}
                  discovered={discovered}
                  done={done}
                  pct={pct}
                  chunks={progress?.chunks_embedded ?? 0}
                  errors={progress?.errors ?? 0}
                />
              ) : (
                <div>
                  <ProfileChip
                    name={session?.user?.name ?? "Developer"}
                    email={session?.user?.email ?? ""}
                    image={session?.user?.image ?? ""}
                  />
                  <div ref={panelRef} className="overflow-hidden">
                    <CortexPanel
                      tab={tab}
                      setTab={setTab}
                      localPath={localPath}
                      setLocalPath={setLocalPath}
                      repoUrl={repoUrl}
                      setRepoUrl={setRepoUrl}
                      pat={pat}
                      setPat={setPat}
                      showPat={showPat}
                      setShowPat={setShowPat}
                      canLaunch={canLaunch}
                      onLaunch={launch}
                      error={error}
                    />
                    <RepoManager onOpen={openRepo} />
                  </div>
                </div>
              )}
            </div>
          </div>
          <p className="mt-4 text-center text-[11px] text-neutral-600">
            Local-first · your code never leaves this machine.
          </p>
        </div>
      </div>
    </div>
  );
}

// ----------------------------------------------------------------------------

function SignInView({ githubEnabled, googleEnabled }: { githubEnabled: boolean; googleEnabled: boolean }) {
  return (
    <div className="space-y-3">
      <div className="mb-4 text-center">
        <div className="text-[10px] uppercase tracking-[0.2em] text-neutral-500">Initialize Session</div>
        <p className="mt-1 text-[13px] text-neutral-400">Authenticate to map your engineering assets.</p>
      </div>

      <OAuthButton
        provider="github"
        label="Initialize via GitHub"
        enabled={githubEnabled}
        icon={
          <svg viewBox="0 0 16 16" className="h-4 w-4" fill="currentColor" aria-hidden>
            <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0016 8c0-4.42-3.58-8-8-8z" />
          </svg>
        }
      />
      <OAuthButton
        provider="google"
        label="Initialize via Google"
        enabled={googleEnabled}
        icon={
          <svg viewBox="0 0 24 24" className="h-4 w-4" aria-hidden>
            <path
              fill="#4285F4"
              d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 01-2.2 3.32v2.77h3.57c2.08-1.92 3.27-4.74 3.27-8.1z"
            />
            <path
              fill="#34A853"
              d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84A11 11 0 0012 23z"
            />
            <path
              fill="#FBBC05"
              d="M5.84 14.1a6.6 6.6 0 010-4.2V7.06H2.18a11 11 0 000 9.88l3.66-2.84z"
            />
            <path
              fill="#EA4335"
              d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.06l3.66 2.84C6.71 7.31 9.14 5.38 12 5.38z"
            />
          </svg>
        }
      />

      <div className="flex items-center gap-3 py-1">
        <span className="h-px flex-1 bg-panel-border" />
        <span className="text-[10px] uppercase tracking-widest text-neutral-600">or</span>
        <span className="h-px flex-1 bg-panel-border" />
      </div>

      <button
        onClick={() => signIn("local", { redirectTo: "/" })}
        className="w-full rounded-lg border border-panel-border bg-neutral-900/60 px-4 py-2.5 text-[13px] text-neutral-300 transition-colors hover:border-neon hover:text-neon"
      >
        Continue as Local Developer
      </button>
    </div>
  );
}

function OAuthButton({
  provider,
  label,
  icon,
  enabled,
}: {
  provider: string;
  label: string;
  icon: React.ReactNode;
  enabled: boolean;
}) {
  if (!enabled) {
    return (
      <div
        title={`Set ${provider.toUpperCase()}_CLIENT_ID and _SECRET in .env.local to enable`}
        className="flex w-full cursor-not-allowed items-center justify-center gap-2.5 rounded-lg border border-panel-border bg-neutral-900/40 px-4 py-2.5 text-[13px] text-neutral-600"
      >
        {icon}
        <span>{label}</span>
        <span className="ml-1 rounded bg-neutral-800 px-1.5 py-0.5 text-[9px] uppercase tracking-wider text-neutral-500">
          not configured
        </span>
      </div>
    );
  }
  return (
    <button
      onClick={() => signIn(provider, { redirectTo: "/" })}
      className="flex w-full items-center justify-center gap-2.5 rounded-lg border border-panel-border bg-white px-4 py-2.5 text-[13px] font-semibold text-black transition-transform hover:scale-[1.01] active:scale-[0.99]"
    >
      {icon}
      <span>{label}</span>
    </button>
  );
}

function ProfileChip({ name, email, image }: { name: string; email: string; image: string }) {
  const initial = (email || name || "?").charAt(0).toUpperCase();
  return (
    <div className="mb-5 flex items-center gap-3 rounded-xl border border-panel-border bg-neutral-900/60 px-3 py-2.5">
      {image ? (
        // eslint-disable-next-line @next/next/no-img-element
        <img src={image} alt="" className="h-8 w-8 rounded-full border border-panel-border object-cover" />
      ) : (
        <span className="flex h-8 w-8 items-center justify-center rounded-full bg-accent text-[13px] font-bold text-white">
          {initial}
        </span>
      )}
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-medium text-neutral-100">{name}</div>
        <div className="truncate font-mono text-[11px] text-neutral-500">{email}</div>
      </div>
      <button
        onClick={() => signOut({ redirectTo: "/" })}
        className="shrink-0 rounded-lg border border-panel-border px-2.5 py-1.5 text-[11px] text-neutral-400 transition-colors hover:border-red-500/50 hover:text-red-300"
      >
        Disconnect
      </button>
    </div>
  );
}

function CortexPanel(props: {
  tab: Tab;
  setTab: (t: Tab) => void;
  localPath: string;
  setLocalPath: (v: string) => void;
  repoUrl: string;
  setRepoUrl: (v: string) => void;
  pat: string;
  setPat: (v: string) => void;
  showPat: boolean;
  setShowPat: (v: boolean) => void;
  canLaunch: boolean;
  onLaunch: () => void;
  error: string | null;
}) {
  const {
    tab,
    setTab,
    localPath,
    setLocalPath,
    repoUrl,
    setRepoUrl,
    pat,
    setPat,
    showPat,
    setShowPat,
    canLaunch,
    onLaunch,
    error,
  } = props;

  return (
    <div>
      <div className="mb-3 text-[10px] uppercase tracking-[0.2em] text-neutral-500">Cortex Connection</div>

      {/* Tab selector */}
      <div className="mb-4 flex rounded-lg border border-panel-border bg-neutral-900/60 p-1">
        {(["local", "repo"] as Tab[]).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={
              "flex-1 rounded-md px-3 py-2 text-[12px] font-medium transition-colors " +
              (tab === t ? "bg-accent text-white" : "text-neutral-400 hover:text-neutral-200")
            }
          >
            {t === "local" ? "Local Direct Path" : "Public Repository Link"}
          </button>
        ))}
      </div>

      {tab === "local" ? (
        <input
          type="text"
          value={localPath}
          onChange={(e) => setLocalPath(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && canLaunch && onLaunch()}
          placeholder="/absolute/path/to/project"
          spellCheck={false}
          autoComplete="off"
          className="w-full rounded-xl border border-panel-border bg-black/60 px-4 py-3 font-mono text-sm text-neutral-100 outline-none placeholder:text-neutral-600 focus:border-accent"
        />
      ) : (
        <div className="space-y-2.5">
          <input
            type="url"
            value={repoUrl}
            onChange={(e) => setRepoUrl(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && canLaunch && onLaunch()}
            placeholder="https://github.com/user/repo.git"
            spellCheck={false}
            autoComplete="off"
            className="w-full rounded-xl border border-panel-border bg-black/60 px-4 py-3 font-mono text-sm text-neutral-100 outline-none placeholder:text-neutral-600 focus:border-accent"
          />
          <div className="rounded-xl border border-panel-border bg-panel/40">
            <button
              type="button"
              onClick={() => setShowPat(!showPat)}
              className="flex w-full items-center justify-between px-3.5 py-2 text-left text-[11px] text-neutral-500 hover:text-neutral-300"
            >
              <span>Private / enterprise repo — access token</span>
              <span className={"transition-transform " + (showPat ? "rotate-90" : "")}>›</span>
            </button>
            {showPat && (
              <div className="border-t border-panel-border px-3.5 py-2.5">
                <input
                  type="password"
                  value={pat}
                  onChange={(e) => setPat(e.target.value)}
                  placeholder="ghp_… (kept server-side, never logged)"
                  autoComplete="off"
                  spellCheck={false}
                  className="w-full rounded-lg border border-panel-border bg-neutral-900 px-3 py-2 font-mono text-[12px] text-neutral-100 outline-none placeholder:text-neutral-600 focus:border-neon"
                />
              </div>
            )}
          </div>
        </div>
      )}

      <button
        onClick={onLaunch}
        disabled={!canLaunch}
        className="group mt-4 flex w-full items-center justify-center gap-2 rounded-xl bg-accent px-4 py-3 text-sm font-semibold text-white transition-all hover:bg-indigo-500 disabled:cursor-not-allowed disabled:opacity-40"
      >
        Synapse Architecture
        <span className="transition-transform group-hover:translate-x-0.5">→</span>
      </button>

      {error && (
        <p className="mt-3 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-[12px] text-red-300">
          {error}
        </p>
      )}
    </div>
  );
}

// RepoManager — collapsible panel on the gateway to open / delete already-ingested
// repositories, or clear the whole database, without entering the workspace first.
function RepoManager({ onOpen }: { onOpen: (root: string) => void }) {
  const [repos, setRepos] = useState<RepoInfo[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState<string | null>(null); // root being deleted, or "*" for clear-all
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setRepos(await fetchRepos());
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "could not reach the backend");
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const remove = useCallback(
    async (root: string) => {
      setBusy(root);
      setErr(null);
      try {
        await deleteRepo(root);
        await refresh();
      } catch (e) {
        setErr(e instanceof Error ? e.message : "delete failed");
      } finally {
        setBusy(null);
        setConfirmDelete(null);
      }
    },
    [refresh],
  );

  const clearAll = useCallback(async () => {
    setBusy("*");
    setErr(null);
    try {
      for (const r of repos) await deleteRepo(r.root_path);
      await refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "clear failed");
    } finally {
      setBusy(null);
      setConfirmClear(false);
    }
  }, [repos, refresh]);

  // Nothing to manage until at least one repo exists.
  if (!loaded || repos.length === 0) return null;

  return (
    <div className="mt-3 overflow-hidden rounded-xl border border-panel-border bg-neutral-900/40">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center justify-between px-3.5 py-2.5 text-left transition-colors hover:bg-neutral-900/60"
      >
        <span className="flex items-center gap-2 text-[12px] text-neutral-300">
          <span className="text-neutral-500">▦</span>
          Manage repositories
          <span className="rounded-full bg-neutral-800 px-1.5 py-0.5 text-[10px] font-medium text-neutral-400">
            {repos.length}
          </span>
        </span>
        <span className={"text-neutral-500 transition-transform " + (open ? "rotate-90" : "")}>›</span>
      </button>

      {open && (
        <div className="space-y-2 border-t border-panel-border px-3 py-3">
          {repos.map((r) => {
            const confirming = confirmDelete === r.root_path;
            return (
              <div
                key={r.root_path}
                className="flex items-center gap-2 rounded-lg border border-panel-border bg-black/40 px-3 py-2"
              >
                <div className="min-w-0 flex-1">
                  <div className="truncate font-mono text-[12px] text-neutral-100">{r.name}</div>
                  <div className="truncate text-[10px] text-neutral-500">
                    {r.files} files · {r.chunks} chunks
                    <span className="text-neutral-600"> · {r.root_path}</span>
                  </div>
                </div>
                <button
                  onClick={() => onOpen(r.root_path)}
                  className="shrink-0 rounded-md border border-panel-border px-2.5 py-1 text-[11px] text-neutral-300 transition-colors hover:border-accent hover:text-accent"
                >
                  Open
                </button>
                {confirming ? (
                  <>
                    <button
                      onClick={() => remove(r.root_path)}
                      disabled={busy !== null}
                      className="shrink-0 rounded-md border border-red-500/50 bg-red-500/10 px-2.5 py-1 text-[11px] text-red-300 transition-colors hover:bg-red-500/20 disabled:opacity-50"
                    >
                      {busy === r.root_path ? "removing…" : "remove"}
                    </button>
                    <button
                      onClick={() => setConfirmDelete(null)}
                      disabled={busy !== null}
                      className="shrink-0 rounded-md border border-panel-border px-2 py-1 text-[11px] text-neutral-400 transition-colors hover:border-neutral-500 disabled:opacity-50"
                    >
                      cancel
                    </button>
                  </>
                ) : (
                  <button
                    onClick={() => setConfirmDelete(r.root_path)}
                    disabled={busy !== null}
                    title="Delete this repository"
                    className="shrink-0 rounded-md border border-panel-border px-2 py-1 text-[11px] leading-none text-neutral-500 transition-colors hover:border-red-500/50 hover:text-red-300 disabled:opacity-50"
                  >
                    ✕
                  </button>
                )}
              </div>
            );
          })}

          {/* Clear the whole database (delete every repository). */}
          <div className="flex items-center justify-end pt-1">
            {confirmClear ? (
              <div className="flex items-center gap-2">
                <span className="text-[11px] text-red-300">Delete all {repos.length}?</span>
                <button
                  onClick={clearAll}
                  disabled={busy !== null}
                  className="rounded-md border border-red-500/50 bg-red-500/10 px-2.5 py-1 text-[11px] text-red-300 transition-colors hover:bg-red-500/20 disabled:opacity-50"
                >
                  {busy === "*" ? "clearing…" : "yes, clear database"}
                </button>
                <button
                  onClick={() => setConfirmClear(false)}
                  disabled={busy !== null}
                  className="rounded-md border border-panel-border px-2.5 py-1 text-[11px] text-neutral-400 transition-colors hover:border-neutral-500 disabled:opacity-50"
                >
                  cancel
                </button>
              </div>
            ) : (
              <button
                onClick={() => setConfirmClear(true)}
                disabled={busy !== null}
                className="rounded-md border border-panel-border px-2.5 py-1 text-[11px] text-neutral-500 transition-colors hover:border-red-500/50 hover:text-red-300 disabled:opacity-50"
              >
                Clear database
              </button>
            )}
          </div>

          {err && <p className="pt-1 text-[11px] text-red-300">{err}</p>}
        </div>
      )}
    </div>
  );
}

function Terminal(props: {
  termRef: React.RefObject<HTMLDivElement | null>;
  stages: string[];
  source: string;
  status: string;
  discovered: number;
  done: number;
  pct: number;
  chunks: number;
  errors: number;
}) {
  const { termRef, stages, source, status, discovered, done, pct, chunks, errors } = props;
  return (
    <div ref={termRef} className="overflow-hidden rounded-xl border border-panel-border bg-black font-mono text-[12.5px]">
      <div className="flex items-center gap-1.5 border-b border-panel-border px-3 py-2">
        <span className="h-2.5 w-2.5 rounded-full bg-red-500/70" />
        <span className="h-2.5 w-2.5 rounded-full bg-amber-500/70" />
        <span className="h-2.5 w-2.5 rounded-full bg-emerald-500/70" />
        <span className="ml-2 truncate text-[11px] text-neutral-600">synapse · ingest · {source || "target"}</span>
      </div>
      <div className="space-y-1.5 px-4 py-4">
        {stages.map((line, i) => (
          <div key={i} className="term-line flex items-center gap-2 text-neutral-300">
            <span className="text-neon">▸</span>
            <span>{line}</span>
          </div>
        ))}

        {/* Live progress */}
        <div className="mt-3 h-1.5 w-full overflow-hidden rounded-full bg-neutral-800">
          <div
            className={"h-full rounded-full bg-accent transition-all duration-500 " + (discovered === 0 ? "w-1/3 animate-pulse" : "")}
            style={discovered > 0 ? { width: `${pct}%` } : undefined}
          />
        </div>
        <div className="flex items-center justify-between text-[11px] text-neutral-500">
          <span>
            {discovered > 0 ? `${done}/${discovered} files · ${chunks} chunks` : "discovering files…"}
            {errors > 0 ? ` · ${errors} skipped` : ""}
          </span>
          <span className="text-neutral-600">
            {status === "done" ? "entering workspace…" : discovered > 0 ? `${pct}%` : ""}
          </span>
        </div>
      </div>
    </div>
  );
}
