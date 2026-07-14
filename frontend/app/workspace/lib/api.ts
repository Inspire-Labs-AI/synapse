// API client for the Project Synapse Go backend (graph topology + RAG query).
// Base URL is overridable at build time via NEXT_PUBLIC_SYNAPSE_API.

export const API_BASE =
  process.env.NEXT_PUBLIC_SYNAPSE_API ?? "http://localhost:8080";

// --- Backend payload shapes (mirror internal/api/graph.go + rag.go) ---------

export interface GraphNode {
  id: string;
  position: { x: number; y: number };
  data: {
    label: string;
    kind: "file" | "module" | "endpoint" | string;
    path?: string;
    language?: string;
    size?: number;
    exports?: number;
    method?: string;
    [key: string]: unknown;
  };
}

export interface GraphEdge {
  id: string;
  source: string;
  target: string;
  label?: string;
  animated?: boolean;
}

export interface GraphData {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export interface FunctionHit {
  file: string;
  symbol: string;
  chunk_type: string;
  start_line: number;
  end_line: number;
  code: string;
}

export interface QueryAnswer {
  answer: string;
  highlighted_files: string[];
  execution_flow: string[];
  functions: FunctionHit[];
}

// --- Fetchers ---------------------------------------------------------------

export async function fetchGraph(
  repo?: string | null,
  signal?: AbortSignal,
): Promise<GraphData> {
  const qs = repo ? `?repo=${encodeURIComponent(repo)}` : "";
  const res = await fetch(`${API_BASE}/api/graph/data${qs}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`graph fetch failed: ${res.status}`);
  }
  const data = (await res.json()) as GraphData;
  return { nodes: data.nodes ?? [], edges: data.edges ?? [] };
}

export async function postQuery(
  question: string,
  repo?: string | null,
  signal?: AbortSignal,
): Promise<QueryAnswer> {
  const res = await fetch(`${API_BASE}/api/query`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ question, repo: repo ?? "" }),
    signal,
  });
  if (!res.ok) {
    throw new Error(`query failed: ${res.status}`);
  }
  return (await res.json()) as QueryAnswer;
}

/** Stable node id for a file path, matching the backend's "file:<path>" ids. */
export function fileNodeId(path: string): string {
  return `file:${path}`;
}

// --- Server-Sent-Events streaming -------------------------------------------

/** Read a fetch SSE body, dispatching each `event:`/`data:` frame. */
async function readSSE(
  body: ReadableStream<Uint8Array>,
  onEvent: (event: string, data: unknown) => void,
): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let sep: number;
    // Frames are separated by a blank line.
    while ((sep = buf.indexOf("\n\n")) >= 0) {
      const frame = buf.slice(0, sep);
      buf = buf.slice(sep + 2);
      let event = "message";
      let dataStr = "";
      for (const line of frame.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) dataStr += line.slice(5).trim();
      }
      if (!dataStr) continue;
      let data: unknown = dataStr;
      try {
        data = JSON.parse(dataStr);
      } catch {
        /* keep raw string */
      }
      onEvent(event, data);
    }
  }
}

/** Metadata emitted before the streamed answer (derived from retrieval). */
export interface QueryMeta {
  highlighted_files: string[];
  execution_flow: string[];
  functions: FunctionHit[];
}

export interface StreamQueryHandlers {
  onMeta?: (meta: QueryMeta) => void;
  onToken?: (delta: string) => void;
}

/** Stream a RAG answer: metadata first, then the markdown answer token-by-token. */
export async function streamQuery(
  question: string,
  repo: string | null,
  handlers: StreamQueryHandlers,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`${API_BASE}/api/query/stream`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
    body: JSON.stringify({ question, repo: repo ?? "" }),
    signal,
  });
  if (!res.ok || !res.body) {
    throw new Error(`query failed: ${res.status}`);
  }
  await readSSE(res.body, (event, data) => {
    if (event === "meta") handlers.onMeta?.(data as QueryMeta);
    else if (event === "token") handlers.onToken?.((data as { delta: string }).delta);
    else if (event === "error") throw new Error((data as { error?: string }).error ?? "stream error");
  });
}

export interface StreamDiscoverHandlers {
  onResult?: (bp: BlueprintResponse) => void;
  onToken?: (delta: string) => void;
}

/** Stream capability discovery: the structured result, then a narrative briefing. */
export type BlueprintMode = "validate" | "roadmap";

export async function streamDiscover(
  description: string,
  repo: string | null,
  mode: BlueprintMode,
  handlers: StreamDiscoverHandlers,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`${API_BASE}/api/blueprint/discover/stream`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
    body: JSON.stringify({ description, repo: repo ?? "", mode }),
    signal,
  });
  if (!res.ok || !res.body) {
    throw new Error(`discover failed: ${res.status}`);
  }
  await readSSE(res.body, (event, data) => {
    if (event === "result") handlers.onResult?.(data as BlueprintResponse);
    else if (event === "token") handlers.onToken?.((data as { delta: string }).delta);
    else if (event === "error") throw new Error((data as { error?: string }).error ?? "stream error");
  });
}

// --- Repositories (multi-repo workspace) ------------------------------------

export interface RepoInfo {
  root_path: string; // canonical id used to scope graph/query/discover
  name: string; // friendly label (trailing path segment)
  files: number;
  chunks: number;
}

/** List the ingested repositories for the workspace switcher. */
export async function fetchRepos(signal?: AbortSignal): Promise<RepoInfo[]> {
  const res = await fetch(`${API_BASE}/api/repos`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`repos fetch failed: ${res.status}`);
  }
  const data = (await res.json()) as { repos?: RepoInfo[] };
  return data.repos ?? [];
}

/** One intra-file call edge: caller symbol -> callee symbol. */
export interface CallEdge {
  caller: string;
  callee: string;
}

/** The symbol-level functions of a file plus its intra-file call graph. */
export interface FileFunctions {
  functions: FunctionHit[];
  calls: CallEdge[];
}

/** Fetch the symbol-level functions (with code) and call graph of one file. */
export async function fetchFileFunctions(
  repo: string | null,
  path: string,
  signal?: AbortSignal,
): Promise<FileFunctions> {
  const params = new URLSearchParams({ path });
  if (repo) params.set("repo", repo);
  const res = await fetch(`${API_BASE}/api/file/functions?${params.toString()}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`file functions fetch failed: ${res.status}`);
  }
  const data = (await res.json()) as { functions?: Omit<FunctionHit, "file">[]; calls?: CallEdge[] };
  // The endpoint scopes to one file but omits the path on each row; stamp it back
  // on so downstream filtering (canvas file-detail) and node ids stay consistent.
  const functions: FunctionHit[] = (data.functions ?? []).map((f) => ({ ...f, file: path }));
  return { functions, calls: data.calls ?? [] };
}

// Short LLM-written summary of a single file's role (canvas detail panel).
export async function fetchFileSummary(
  repo: string | null,
  path: string,
  signal?: AbortSignal,
): Promise<string> {
  const params = new URLSearchParams({ path });
  if (repo) params.set("repo", repo);
  const res = await fetch(`${API_BASE}/api/file/summary?${params.toString()}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) throw new Error(`file summary fetch failed: ${res.status}`);
  const data = (await res.json()) as { summary?: string };
  return data.summary ?? "";
}

// --- Axon Pathway (dependency-ordered onboarding tour) ----------------------

export interface AxonStep {
  order: number;
  file: string;
  label: string;
  role: string; // foundation | core | entry | consumer | docs
  summary: string;
  symbols: string[];
  imports: string[];
  imported_by: string[];
}

export interface AxonPathway {
  repo: string;
  name: string;
  intro: string;
  steps: AxonStep[];
}

export async function fetchPathway(repo: string, signal?: AbortSignal): Promise<AxonPathway> {
  const res = await fetch(`${API_BASE}/api/axon/pathway?repo=${encodeURIComponent(repo)}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`pathway fetch failed: ${res.status}`);
  }
  const data = (await res.json()) as AxonPathway;
  return {
    ...data,
    intro: data.intro ?? "",
    steps: (data.steps ?? []).map((s) => ({
      ...s,
      symbols: s.symbols ?? [],
      imports: s.imports ?? [],
      imported_by: s.imported_by ?? [],
    })),
  };
}

// --- Documentation ----------------------------------------------------------

export interface DocSection {
  id: string;
  title: string;
  group: string; // sidebar grouping
  content: string; // markdown
}

export interface RepoDocs {
  repo: string;
  name: string;
  sections: DocSection[];
}

export async function fetchDocs(repo: string, signal?: AbortSignal): Promise<RepoDocs> {
  const res = await fetch(`${API_BASE}/api/docs?repo=${encodeURIComponent(repo)}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`docs fetch failed: ${res.status}`);
  }
  return (await res.json()) as RepoDocs;
}

// --- Architecture -----------------------------------------------------------

export interface ArchComponent {
  id: string;
  name: string;
  layer: string; // frontend | backend | data | external | shared
  description: string;
  tech: string[];
  files: string[];
}

export interface ArchEdge {
  source: string;
  target: string;
  label: string;
  description?: string; // what data flows across this edge and why
}

export interface RepoArchitecture {
  repo: string;
  name: string;
  summary: string;
  pattern?: string; // the architectural style, in a few words
  design?: string; // markdown system-design narrative
  components: ArchComponent[];
  edges: ArchEdge[];
}

export async function fetchArchitecture(
  repo: string,
  signal?: AbortSignal,
): Promise<RepoArchitecture> {
  const res = await fetch(`${API_BASE}/api/architecture?repo=${encodeURIComponent(repo)}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`architecture fetch failed: ${res.status}`);
  }
  const data = (await res.json()) as RepoArchitecture;
  // Guard against null slices (Go marshals nil slices as null, not []).
  return {
    ...data,
    components: (data.components ?? []).map((c) => ({ ...c, files: c.files ?? [], tech: c.tech ?? [] })),
    edges: data.edges ?? [],
  };
}

// --- Synaptic Pruning (dead-code analysis) ----------------------------------

export interface PruneCandidate {
  kind: string; // file | export | function
  tier: string; // orphan_file | dead_cluster | unused_export | unused_function
  path: string;
  symbol?: string;
  language: string;
  confidence: string; // high | medium
  reason: string;
  evidence: string[];
  uncertain: boolean;
}

export interface PruneReport {
  repo: string;
  name: string;
  total_files: number;
  code_files: number;
  entry_points: string[];
  candidates: PruneCandidate[];
  summary: Record<string, number>;
  notes: string[];
}

/** Fetch the dead-code ("Synaptic Pruning") analysis for a repo. */
export async function fetchPrune(repo: string, signal?: AbortSignal): Promise<PruneReport> {
  const res = await fetch(`${API_BASE}/api/prune?repo=${encodeURIComponent(repo)}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`prune fetch failed: ${res.status}`);
  }
  const data = (await res.json()) as PruneReport;
  return {
    ...data,
    candidates: (data.candidates ?? []).map((c) => ({ ...c, evidence: c.evidence ?? [] })),
    entry_points: data.entry_points ?? [],
    notes: data.notes ?? [],
    summary: data.summary ?? {},
  };
}

// --- Bug & anti-pattern detection -------------------------------------------

export interface BugLocation {
  file: string;
  line_start: number;
  line_end: number;
  entity: string;
}
export interface BugFinding {
  issue: string;
  impact: string;
  fix: string;
}
export interface Bug {
  bug_id: string; // SYN-YYYY-NNN
  title: string;
  severity: string; // CRITICAL | HIGH | MEDIUM | LOW
  category: string; // circular_dependency | resource_leak | bad_practice | security | logic | concurrency | ...
  tier: string; // deterministic | verified | llm
  confidence: string; // high | medium | low
  location: BugLocation;
  finding: BugFinding;
  context_nodes: string[];
}
export interface BugReport {
  repo: string;
  name: string;
  scanned: number;
  bugs: Bug[];
  summary: Record<string, number>;
  notes: string[];
}

/** Run the two-tier bug + anti-pattern scan for a repo. */
export async function fetchBugs(repo: string, signal?: AbortSignal): Promise<BugReport> {
  const res = await fetch(`${API_BASE}/api/bugs?repo=${encodeURIComponent(repo)}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`bug scan failed: ${res.status}`);
  }
  const data = (await res.json()) as BugReport;
  return {
    ...data,
    bugs: (data.bugs ?? []).map((b) => ({ ...b, context_nodes: b.context_nodes ?? [] })),
    summary: data.summary ?? {},
    notes: data.notes ?? [],
  };
}

/** Remove one ingested repository by its root_path. */
export async function deleteRepo(
  rootPath: string,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(
    `${API_BASE}/api/repos?repo=${encodeURIComponent(rootPath)}`,
    { method: "DELETE", headers: { Accept: "application/json" }, signal },
  );
  if (!res.ok) {
    throw new Error(`repo delete failed: ${res.status}`);
  }
}

// --- Blueprint discovery (Phase 5) ------------------------------------------

export type BlueprintCategory = "green" | "yellow" | "red";

export interface BlueprintMatch {
  kind: string; // entity | action
  name: string;
  category: BlueprintCategory;
  confidence: number;
  files: string[];
  symbols: string[];
  endpoints: string[];
  recommendation: string;
}

export interface BlueprintGap {
  id: string;
  label: string;
  kind: string;
  reason: string;
  suggested_file: string;
}

export interface BlueprintGapEdge {
  source: string;
  target: string;
}

export interface BlueprintDiff {
  file: string;
  change_type: "extend" | "create";
  category: BlueprintCategory;
  detail: string;
}

export interface BlueprintResponse {
  description: string;
  intents: { entities: { name: string }[]; actions: { name: string }[] };
  matches: BlueprintMatch[];
  summary: {
    green: number;
    yellow: number;
    red: number;
    total: number;
    reuse_score: number;
  };
  highlights: { green: string[]; yellow: string[] };
  gaps: BlueprintGap[];
  gap_edges: BlueprintGapEdge[];
  diff_summary: BlueprintDiff[];
}

// --- Repository ingestion (clone-on-demand) ---------------------------------

export interface IngestJob {
  job_id: string;
  status: string;
}

export interface IngestStatus {
  job_id: string;
  status: "queued" | "cloning" | "ingesting" | "done" | "error";
  phase: string;
  repo: string;
  root_path: string;
  files_discovered: number;
  files_done: number;
  chunks_embedded: number;
  errors: number;
  error?: string;
}

/** Ingestion source: either a public/private repo URL or a local directory. */
export interface IngestInput {
  repoUrl?: string;
  pat?: string;
  localPath?: string;
}

/** Start an asynchronous ingest; returns a job id to poll (does not block). */
export async function startIngest(
  input: IngestInput,
  signal?: AbortSignal,
): Promise<IngestJob> {
  const res = await fetch(`${API_BASE}/api/ingest`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({
      repo_url: input.repoUrl ?? "",
      pat: input.pat ?? "",
      local_path: input.localPath ?? "",
    }),
    signal,
  });
  if (!res.ok) {
    let detail = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body?.error) detail = body.detail ? `${body.error}: ${body.detail}` : body.error;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(detail);
  }
  return (await res.json()) as IngestJob;
}

/** Poll the progress of an ingest job. */
export async function fetchIngestStatus(jobId: string, signal?: AbortSignal): Promise<IngestStatus> {
  const res = await fetch(`${API_BASE}/api/ingest/status?job=${encodeURIComponent(jobId)}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`status fetch failed: ${res.status}`);
  }
  return (await res.json()) as IngestStatus;
}

export async function postDiscover(
  description: string,
  repo?: string | null,
  signal?: AbortSignal,
): Promise<BlueprintResponse> {
  const res = await fetch(`${API_BASE}/api/blueprint/discover`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ description, repo: repo ?? "" }),
    signal,
  });
  if (!res.ok) {
    throw new Error(`discover failed: ${res.status}`);
  }
  return (await res.json()) as BlueprintResponse;
}

// --- Exports ----------------------------------------------------------------

/** The portable project brief served by GET /api/context (schema synapse.agent-context/v1). */
export interface ProjectContext {
  schema: string;
  repo: string;
  name: string;
  generated_at: string;
  how_to_use: string;
  stats: {
    total_files: number;
    code_files: number;
    doc_files: number;
    endpoints: number;
    internal_import_edges: number;
    external_dependencies: number;
  };
  languages: { language: string; files: number }[];
  entry_points: string[];
  endpoints: { method?: string; path: string; file: string; handler?: string }[];
  modules: { path: string; files: number; key_exports?: string[] }[];
  key_files: { path: string; language: string; imported_by: number; exports?: string[] }[];
  dependencies: { name: string; files: number }[];
  docs?: { title: string; group?: string; content: string }[];
}

/**
 * Fetch the whole-project context brief — structural facts from the AST graph
 * plus the generated documentation — for handing to another AI agent.
 */
export async function fetchProjectContext(repo: string, signal?: AbortSignal): Promise<ProjectContext> {
  const res = await fetch(`${API_BASE}/api/context?repo=${encodeURIComponent(repo)}`, {
    signal,
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error(`context export failed: ${res.status}`);
  }
  return (await res.json()) as ProjectContext;
}

/** Trigger a client-side download of `data` as pretty-printed JSON. */
export function downloadJSON(filename: string, data: unknown): void {
  const url = URL.createObjectURL(
    new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }),
  );
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

/** `my-repo` -> `my-repo-diagnostics-2026-07-10.json` */
export function exportFilename(name: string, kind: string): string {
  const safe = (name || "repo").replace(/[^a-zA-Z0-9._-]+/g, "-").replace(/^-+|-+$/g, "");
  return `${safe || "repo"}-${kind}-${new Date().toISOString().slice(0, 10)}.json`;
}
