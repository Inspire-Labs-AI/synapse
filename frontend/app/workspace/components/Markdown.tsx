"use client";

import type { ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";

// slugify must match the TOC builder so in-page anchor links resolve.
export function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/(^-+|-+$)/g, "");
}

// Flatten React children to plain text (for heading slug ids).
function nodeText(node: ReactNode): string {
  if (node == null || node === false) return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(nodeText).join("");
  if (typeof node === "object" && "props" in node) {
    return nodeText((node as { props?: { children?: ReactNode } }).props?.children);
  }
  return "";
}

// Custom renderers give the docs an "official documentation site" look: anchored
// headings, styled tables/callouts/lists, and syntax-highlighted code blocks.
const components: Components = {
  h1: ({ children }) => (
    <h1 id={slugify(nodeText(children))} className="mb-4 mt-0 scroll-mt-20 border-b border-panel-border pb-2.5 text-[26px] font-bold tracking-tight text-neutral-50">
      {children}
    </h1>
  ),
  h2: ({ children }) => (
    <h2 id={slugify(nodeText(children))} className="mb-3 mt-9 scroll-mt-20 border-b border-panel-border/60 pb-1.5 text-[18px] font-semibold tracking-tight text-neutral-100">
      {children}
    </h2>
  ),
  h3: ({ children }) => (
    <h3 id={slugify(nodeText(children))} className="mb-2 mt-6 scroll-mt-20 text-[15px] font-semibold text-neutral-100">
      {children}
    </h3>
  ),
  h4: ({ children }) => (
    <h4 className="mb-1.5 mt-4 text-[12px] font-semibold uppercase tracking-wider text-neutral-400">{children}</h4>
  ),
  p: ({ children }) => <p className="my-3 text-[14px] leading-7 text-neutral-300">{children}</p>,
  a: ({ href, children }) => (
    <a href={href} target={href?.startsWith("http") ? "_blank" : undefined} rel="noreferrer" className="text-accent underline decoration-accent/40 underline-offset-2 transition-colors hover:decoration-accent">
      {children}
    </a>
  ),
  ul: ({ children }) => <ul className="my-3 space-y-1.5 pl-5 text-[14px] leading-7 text-neutral-300 [list-style:disc] marker:text-neutral-600">{children}</ul>,
  ol: ({ children }) => <ol className="my-3 space-y-1.5 pl-5 text-[14px] leading-7 text-neutral-300 [list-style:decimal] marker:text-neutral-500">{children}</ol>,
  li: ({ children }) => <li className="pl-1">{children}</li>,
  strong: ({ children }) => <strong className="font-semibold text-neutral-100">{children}</strong>,
  em: ({ children }) => <em className="italic text-neutral-300">{children}</em>,
  hr: () => <hr className="my-7 border-panel-border" />,
  blockquote: ({ children }) => (
    <blockquote className="my-4 rounded-r-lg border-l-2 border-accent/60 bg-accent/[0.05] py-1.5 pl-4 pr-3 text-[13.5px] text-neutral-300 [&_p]:my-1.5">
      {children}
    </blockquote>
  ),
  code: ({ className, children }) => {
    const isBlock = /hljs|language-/.test(className ?? "");
    if (isBlock) {
      return <code className={(className ?? "") + " font-mono"}>{children}</code>;
    }
    return (
      <code className="rounded border border-panel-border bg-black/50 px-1.5 py-0.5 font-mono text-[0.84em] text-indigo-300">{children}</code>
    );
  },
  pre: ({ children }) => (
    <pre className="my-4 overflow-x-auto rounded-xl border border-panel-border bg-black/80 p-4 text-[12.5px] leading-relaxed shadow-inner">
      {children}
    </pre>
  ),
  table: ({ children }) => (
    <div className="my-4 overflow-x-auto rounded-lg border border-panel-border">
      <table className="w-full border-collapse text-[13px]">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead className="bg-neutral-900/70">{children}</thead>,
  th: ({ children }) => <th className="border-b border-panel-border px-3 py-2 text-left font-semibold text-neutral-200">{children}</th>,
  td: ({ children }) => <td className="border-b border-panel-border/60 px-3 py-2 align-top text-neutral-300">{children}</td>,
};

/** Markdown — the shared rich renderer for docs + the architecture design narrative. */
export default function Markdown({ children, className = "" }: { children: string; className?: string }) {
  return (
    <div className={className}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeHighlight]} components={components}>
        {children}
      </ReactMarkdown>
    </div>
  );
}
