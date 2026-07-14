"use client";

import { memo } from "react";
import { Handle, Position, useStore, type NodeProps } from "@xyflow/react";
import { ChevronDown, ChevronRight, Folder, Package } from "lucide-react";

import type { GroupFlowNode } from "./types";
import { detailLevel, entityColor } from "./semantic";
import { useGraphActions } from "./GraphActions";

const BORDER = "#262630";

function GroupNodeImpl({ id, data }: NodeProps<GroupFlowNode>) {
  const level = useStore((s) => detailLevel(s.transform[2]));
  const { toggleCollapse } = useGraphActions();

  const color = entityColor(data.entityType);
  const Icon = data.entityType === "directory" ? Folder : Package;
  const collapsed = !!data.collapsed;
  const focal = data.role === "focal";

  return (
    <div
      className="relative h-full w-full rounded-xl border"
      style={{
        borderColor: focal ? color : BORDER,
        // Expanded containers get a faint wash so the boundary reads clearly
        // behind their children; collapsed ones look like a solid pill.
        background: collapsed ? "#0e0e13" : `${color}0a`,
        ...(focal ? { boxShadow: `0 0 0 1px ${color}, 0 0 22px -6px ${color}` } : {}),
      }}
    >
      <Handle type="target" position={Position.Left} className="!h-1.5 !w-1.5 !border-0" style={{ background: color }} />

      <div
        className="flex items-center gap-2 px-3"
        style={{
          height: 46,
          borderBottom: collapsed ? "none" : `1px solid ${BORDER}`,
        }}
      >
        <button
          onClick={() => toggleCollapse(id)}
          title={collapsed ? "Expand group" : "Collapse group"}
          className="shrink-0 rounded-md p-1 text-neutral-300 transition-colors hover:bg-white/10 hover:text-white"
        >
          {collapsed ? <ChevronRight size={15} /> : <ChevronDown size={15} />}
        </button>
        <span className="grid h-6 w-6 shrink-0 place-items-center rounded-md" style={{ background: color + "1f" }}>
          <Icon size={13} style={{ color }} />
        </span>
        <span className="truncate font-mono text-[13px] font-semibold text-neutral-100">{data.name}</span>
        {level !== "macro" && (
          <span
            className="ml-auto shrink-0 rounded-full px-2 py-0.5 text-[10px] font-semibold"
            style={{ background: color + "1f", color }}
          >
            {data.childCount}
          </span>
        )}
      </div>

      <Handle type="source" position={Position.Right} className="!h-1.5 !w-1.5 !border-0" style={{ background: color }} />
    </div>
  );
}

export const GroupNode = memo(GroupNodeImpl);
