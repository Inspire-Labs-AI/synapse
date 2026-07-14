"use client";

import { useState } from "react";

import type { RepoInfo } from "../lib/api";

interface RepoSelectorProps {
  repos: RepoInfo[];
  active: string | null;
  onSelect: (rootPath: string) => void;
  onDelete: (rootPath: string) => void;
}

// Header control for the multi-repo workspace: pick which ingested repository
// the canvas, chat, and blueprint all operate on, and remove ones you're done
// with (two-step confirm so a stray click can't wipe a repo).
export default function RepoSelector({
  repos,
  active,
  onSelect,
  onDelete,
}: RepoSelectorProps) {
  const [confirming, setConfirming] = useState(false);
  const current = repos.find((r) => r.root_path === active) ?? null;

  if (repos.length === 0) {
    return <span className="font-mono text-[11px] text-neutral-600">no repositories</span>;
  }

  return (
    <div className="flex items-center gap-1.5">
      <span className="text-[10px] uppercase tracking-widest text-neutral-600">repo</span>

      <div className="relative">
        <select
          value={active ?? ""}
          onChange={(e) => {
            setConfirming(false);
            onSelect(e.target.value);
          }}
          className="appearance-none rounded-md border border-panel-border bg-neutral-900 py-1 pl-2.5 pr-7 font-mono text-[12px] text-neutral-100 outline-none transition-colors hover:border-accent focus:border-accent"
        >
          {repos.map((r) => (
            <option key={r.root_path} value={r.root_path}>
              {r.name} · {r.files}f
            </option>
          ))}
        </select>
        <span className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[9px] text-neutral-500">
          ▾
        </span>
      </div>

      {current &&
        (confirming ? (
          <div className="flex items-center gap-1">
            <button
              onClick={() => {
                onDelete(current.root_path);
                setConfirming(false);
              }}
              className="rounded-md border border-red-500/50 bg-red-500/10 px-1.5 py-1 text-[10px] text-red-300 transition-colors hover:bg-red-500/20"
              title={`Remove ${current.name}`}
            >
              remove
            </button>
            <button
              onClick={() => setConfirming(false)}
              className="rounded-md border border-panel-border px-1.5 py-1 text-[10px] text-neutral-400 transition-colors hover:border-neutral-500"
            >
              cancel
            </button>
          </div>
        ) : (
          <button
            onClick={() => setConfirming(true)}
            className="rounded-md border border-panel-border px-1.5 py-1 text-[11px] leading-none text-neutral-500 transition-colors hover:border-red-500/50 hover:text-red-300"
            title="Remove this repository"
          >
            ✕
          </button>
        ))}
    </div>
  );
}
