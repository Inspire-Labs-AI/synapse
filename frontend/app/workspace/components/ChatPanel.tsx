"use client";

import { memo, useCallback, useEffect, useRef, useState } from "react";
import { gsap } from "gsap";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

import { streamQuery, type FunctionHit, type QueryAnswer, type QueryMeta } from "../lib/api";
import FunctionList from "./FunctionList";

type Role = "user" | "assistant" | "error";

interface Message {
  id: number;
  role: Role;
  text: string;
  files?: string[];
  functions?: FunctionHit[];
}

interface ChatPanelProps {
  repo: string | null; // active repo root_path to scope queries to
  onResult: (answer: QueryAnswer) => void;
  onFocusFile?: (file: string) => void;
}

const seed: Message[] = [
  {
    id: 0,
    role: "assistant",
    text: "Ask me about your codebase — routes, dependencies, or where a symbol is defined. I'll trace it on the canvas.",
  },
];

// Memoized so a streamed token re-renders ONLY the streaming message (and its
// markdown), not every message in the list — keeps the panel smooth.
const MessageBubble = memo(function MessageBubble({
  m,
  streaming,
  onFocusFile,
}: {
  m: Message;
  streaming: boolean;
  onFocusFile?: (file: string) => void;
}) {
  return (
    <div className={"flex min-w-0 " + (m.role === "user" ? "justify-end" : "justify-start")}>
      <div
        className={
          "min-w-0 max-w-[90%] overflow-hidden break-words rounded-2xl px-3.5 py-2 text-[13px] leading-relaxed " +
          (m.role === "user"
            ? "bg-accent text-white"
            : m.role === "error"
              ? "border border-red-500/40 bg-red-500/10 text-red-300"
              : "border border-panel-border bg-neutral-900 text-neutral-200")
        }
      >
        {m.role === "assistant" ? (
          <div className="md min-w-0">
            {m.text === "" && streaming ? (
              <span className="inline-flex items-center gap-1 py-0.5">
                <span className="dot-pulse h-1.5 w-1.5 rounded-full bg-accent" />
                <span className="dot-pulse h-1.5 w-1.5 rounded-full bg-accent [animation-delay:0.15s]" />
                <span className="dot-pulse h-1.5 w-1.5 rounded-full bg-accent [animation-delay:0.3s]" />
              </span>
            ) : (
              <>
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{m.text}</ReactMarkdown>
                {streaming && (
                  <span className="ml-0.5 inline-block h-3.5 w-[2px] -translate-y-px animate-pulse bg-accent align-middle" />
                )}
              </>
            )}
            {m.functions && m.functions.length > 0 && (
              <FunctionList functions={m.functions} onFocusFile={onFocusFile} />
            )}
          </div>
        ) : (
          m.text
        )}
      </div>
    </div>
  );
});

export default function ChatPanel({ repo, onResult, onFocusFile }: ChatPanelProps) {
  const [messages, setMessages] = useState<Message[]>(seed);
  const [input, setInput] = useState("");
  const [loading, setLoading] = useState(false);
  const [streamingId, setStreamingId] = useState<number | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const nextId = useRef(1);
  const animatedCount = useRef(seed.length);

  // Token batching: accumulate deltas and flush at most once per animation frame
  // so a fast token stream doesn't trigger a render (and markdown re-parse) per token.
  const accRef = useRef("");
  const streamMsgId = useRef<number | null>(null);
  const rafRef = useRef<number | null>(null);

  const flushTokens = useCallback(() => {
    rafRef.current = null;
    const id = streamMsgId.current;
    if (id == null) return;
    const text = accRef.current;
    setMessages((prev) => prev.map((m) => (m.id === id && m.text !== text ? { ...m, text } : m)));
  }, []);

  const scheduleFlush = useCallback(() => {
    if (rafRef.current == null) {
      rafRef.current = requestAnimationFrame(flushTokens);
    }
  }, [flushTokens]);

  useEffect(() => () => {
    if (rafRef.current != null) cancelAnimationFrame(rafRef.current);
  }, []);

  // Animate only newly-added messages (not token updates), and stay pinned to
  // the bottom so the streaming answer remains in view.
  useEffect(() => {
    const list = listRef.current;
    if (!list) return;
    if (messages.length > animatedCount.current) {
      const last = list.lastElementChild;
      if (last) {
        gsap.fromTo(last, { opacity: 0, y: 10 }, { opacity: 1, y: 0, duration: 0.35, ease: "power2.out" });
      }
      animatedCount.current = messages.length;
    }
    list.scrollTop = list.scrollHeight;
  }, [messages]);

  async function send(e: React.FormEvent) {
    e.preventDefault();
    const question = input.trim();
    if (!question || loading) return;

    const userId = nextId.current++;
    const aId = nextId.current++;
    streamMsgId.current = aId;
    accRef.current = "";
    setMessages((prev) => [
      ...prev,
      { id: userId, role: "user", text: question },
      { id: aId, role: "assistant", text: "" },
    ]);
    setInput("");
    setLoading(true);
    setStreamingId(aId);

    let meta: QueryMeta | null = null;
    try {
      await streamQuery(question, repo, {
        // Hold the supporting evidence (responsible functions, execution trace,
        // targets, canvas highlights) until the answer finishes — so they reveal
        // in coordination with the completed response instead of popping in first.
        onMeta: (m) => {
          meta = m;
        },
        onToken: (delta) => {
          accRef.current += delta;
          scheduleFlush();
        },
      });
      // Final flush so the complete answer is rendered.
      if (rafRef.current != null) {
        cancelAnimationFrame(rafRef.current);
        rafRef.current = null;
      }
      flushTokens();
      // Coordinated reveal: attach the functions to the message and push the
      // execution trace / targets / canvas highlights together, now.
      if (meta) {
        const m: QueryMeta = meta;
        setMessages((prev) =>
          prev.map((msg) => (msg.id === aId ? { ...msg, files: m.highlighted_files, functions: m.functions } : msg)),
        );
        onResult({ answer: accRef.current, highlighted_files: m.highlighted_files, execution_flow: m.execution_flow, functions: m.functions });
      }
    } catch (err) {
      const text = `Query failed: ${err instanceof Error ? err.message : "unknown error"}. Is the backend running on :8080?`;
      setMessages((prev) => prev.map((m) => (m.id === aId ? { ...m, role: "error" as Role, text } : m)));
    } finally {
      setLoading(false);
      setStreamingId(null);
      streamMsgId.current = null;
    }
  }

  return (
    <div className="flex h-full min-w-0 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-panel-border px-4 py-3">
        <span className="inline-block h-2 w-2 rounded-full bg-accent shadow-[0_0_10px_2px_var(--color-accent)]" />
        <span className="font-mono text-xs font-semibold tracking-tight text-neutral-200">
          synapse assistant
        </span>
        <span className="text-[10px] uppercase tracking-widest text-neutral-600">
          hybrid rag
        </span>
      </div>

      <div ref={listRef} className="min-w-0 flex-1 space-y-3 overflow-y-auto overflow-x-hidden px-4 py-4">
        {messages.map((m) => (
          <MessageBubble key={m.id} m={m} streaming={streamingId === m.id} onFocusFile={onFocusFile} />
        ))}
      </div>

      <form
        onSubmit={send}
        className="flex shrink-0 items-center gap-2 border-t border-panel-border p-3"
      >
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Where is the /category route handled?"
          disabled={loading}
          className="min-w-0 flex-1 rounded-lg border border-panel-border bg-neutral-900 px-3 py-2 text-[13px] text-neutral-100 outline-none placeholder:text-neutral-600 focus:border-accent disabled:opacity-50"
        />
        <button
          type="submit"
          disabled={!input.trim() || loading}
          className="shrink-0 rounded-lg bg-accent px-3.5 py-2 text-[13px] font-medium text-white transition-colors hover:bg-indigo-500 disabled:opacity-40"
        >
          Send
        </button>
      </form>
    </div>
  );
}
