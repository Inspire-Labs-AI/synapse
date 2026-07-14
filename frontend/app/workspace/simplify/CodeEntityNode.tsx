"use client";

import { memo } from "react";
import { Handle, Position, useStore, type NodeProps } from "@xyflow/react";
import {
  Box,
  Braces,
  ChevronDown,
  ChevronRight,
  Crosshair,
  FileCode,
  Globe,
  Hexagon,
  Variable,
  type LucideIcon,
} from "lucide-react";

import type { EntityFlowNode, EntityType } from "./types";
import { detailLevel, entityColor } from "./semantic";
import { useGraphActions } from "./GraphActions";

const ICON: Record<EntityType, LucideIcon> = {
  directory: Box,
  module: Box,
  file: FileCode,
  class: Box,
  interface: Hexagon,
  function: Variable,
  method: Braces,
  endpoint: Globe,
};

const TYPE_LABEL: Record<EntityType, string> = {
  directory: "dir",
  module: "module",
  file: "file",
  class: "class",
  interface: "interface",
  function: "fn",
  method: "method",
  endpoint: "route",
};

const BORDER = "#262630";

function RoleTag({ role }: { role: "caller" | "dependency" }) {
  const c = role === "caller" ? "#34d399" : "#f472b6";
  return (
    <span
      className="shrink-0 rounded px-1.5 py-0.5 text-[8px] font-bold uppercase tracking-wider"
      style={{ background: c + "1f", color: c }}
    >
      {role === "caller" ? "calls in" : "depends"}
    </span>
  );
}

function CodeEntityNodeImpl({ id, data }: NodeProps<EntityFlowNode>) {
  // Bucketed zoom selector: re-renders ONLY on a level transition, not per frame.
  const level = useStore((s) => detailLevel(s.transform[2]));
  const { focusNode, toggleCollapse } = useGraphActions();

  const color = entityColor(data.entityType);
  const Icon = ICON[data.entityType] ?? FileCode;
  const focal = data.role === "focal";
  const role = data.role === "caller" || data.role === "dependency" ? data.role : undefined;

  const shell =
    "relative flex h-full w-full flex-col overflow-hidden rounded-xl border bg-[#0e0e13] shadow-[0_4px_16px_-6px_rgba(0,0,0,0.7)]";
  const borderColor = focal ? color : BORDER;
  const glow = focal ? { boxShadow: `0 0 0 1px ${color}, 0 0 22px -6px ${color}` } : undefined;

  const targetHandle = (
    <Handle type="target" position={Position.Left} className="!h-1.5 !w-1.5 !border-0" style={{ background: color }} />
  );
  const sourceHandle = (
    <Handle type="source" position={Position.Right} className="!h-1.5 !w-1.5 !border-0" style={{ background: color }} />
  );

  // --- Container mode: an expandable code entity (e.g. a file of functions) ---
  if (data.expandable) {
    return (
      <div className={shell} style={{ borderColor, ...glow }}>
        {targetHandle}
        <div
          className="flex items-center gap-2 border-b px-3 py-2.5"
          style={{ borderColor: BORDER, background: color + "0f" }}
        >
          <span className="grid h-6 w-6 shrink-0 place-items-center rounded-md" style={{ background: color + "22" }}>
            <Icon size={13} style={{ color }} />
          </span>
          <span className="truncate font-mono text-[12.5px] font-semibold text-neutral-100">{data.name}</span>
          {data.collapsed && (
            <span className="shrink-0 font-mono text-[10px] text-neutral-500">{data.childCount} ƒ</span>
          )}
          <button
            onClick={() => toggleCollapse(id)}
            title={data.collapsed ? "Make Focus Node — expand subgraph" : "Collapse subgraph"}
            className="ml-auto shrink-0 rounded-md p-1 text-neutral-400 transition-colors hover:bg-white/10 hover:text-neutral-100"
          >
            {data.collapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} />}
          </button>
        </div>
        {sourceHandle}
      </div>
    );
  }

  // --- Leaf mode: fixed-footprint card, content distributed by detail level ---
  return (
    <div className={shell} style={{ borderColor, ...glow }}>
      {targetHandle}

      {level === "macro" ? (
        // Macro: a compressed colored chip — name only, vertically centered.
        <div className="flex h-full items-center gap-2.5 px-3">
          <span className="h-2.5 w-2.5 shrink-0 rounded-full" style={{ background: color }} />
          <span className="truncate font-mono text-[13px] font-medium text-neutral-100">{data.name}</span>
        </div>
      ) : (
        <>
          {/* Header (top) */}
          <div className="flex items-center gap-2 px-3 pt-2.5">
            <span className="grid h-6 w-6 shrink-0 place-items-center rounded-md" style={{ background: color + "1f" }}>
              <Icon size={13} style={{ color }} />
            </span>
            <span className="truncate font-mono text-[12.5px] font-semibold text-neutral-100">{data.name}</span>
            {role && <RoleTag role={role} />}
          </div>

          {/* Detail (middle, fills remaining height) */}
          <div className="min-h-0 flex-1 px-3 py-1.5">
            {level === "micro" && (data.summary || data.snippet) && (
              <p className="line-clamp-2 text-[11px] leading-snug text-neutral-400">{data.summary ?? data.snippet}</p>
            )}
          </div>

          {/* Meta + action (bottom) */}
          <div className="flex items-center gap-2 border-t px-3 py-2" style={{ borderColor: BORDER }}>
            <span
              className="shrink-0 rounded px-1.5 py-0.5 text-[9px] font-bold uppercase tracking-wide"
              style={{ background: color + "1f", color }}
            >
              {TYPE_LABEL[data.entityType]}
            </span>
            {typeof data.loc === "number" && (
              <span className="shrink-0 font-mono text-[10px] text-neutral-500">{data.loc} loc</span>
            )}
            {data.package && (
              <span className="truncate font-mono text-[10px] text-neutral-600">{data.package}</span>
            )}
            {level === "micro" && (
              <button
                onClick={() => focusNode(id)}
                title="Make Focus Node"
                className="ml-auto flex shrink-0 items-center gap-1 rounded-md border px-1.5 py-0.5 text-[10px] font-medium transition-colors hover:bg-white/5"
                style={{ borderColor: color + "55", color }}
              >
                <Crosshair size={10} /> Focus
              </button>
            )}
          </div>
        </>
      )}

      {sourceHandle}
    </div>
  );
}

export const CodeEntityNode = memo(CodeEntityNodeImpl);
