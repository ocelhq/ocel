"use client";

import type { DeploymentApp, DeploymentResource } from "@console/db/schema";
import { ArrowSquareOutIcon } from "@phosphor-icons/react";
import { Handle, type Node, type NodeProps, Position } from "@xyflow/react";
import { createContext, type ReactNode, useContext } from "react";
import { useIsMobile } from "@/hooks/use-mobile";
import { labelType } from "../../../label";
import { AppMark, OutcomeDot, ProviderMark, ResourceMark } from "../../../marks";

const SelectNode = createContext<(id: string) => void>(() => {});

export const SelectNodeProvider = SelectNode.Provider;

export const desktopFit = { padding: 0.12, maxZoom: 1 };

export const mobileFit = { padding: 0.08, minZoom: 0.75, maxZoom: 1 };

export function useFitOptions() {
  return useIsMobile() ? mobileFit : desktopFit;
}

export function count(n: number, noun: string) {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

export const APP_SIZE = { width: 264, height: 112 };
export const RESOURCE_SIZE = { width: 232, height: 92 };
export const GHOST_SIZE = { width: 200, height: 72 };

function Chip({ children }: { children: ReactNode }) {
  return (
    <span className={`${labelType} border border-border bg-background px-1.5 py-0.5`}>
      {children}
    </span>
  );
}

function tileClass(selected: boolean, ghost: boolean, fill: string) {
  const border = ghost
    ? "border-dashed border-dim/60 text-muted-foreground"
    : selected
      ? "border-foreground"
      : "border-border hover:border-dim";
  const surface = ghost ? "bg-background" : fill;
  return `relative flex size-full flex-col justify-between border p-3 ${surface} ${border}`;
}

const surface =
  "absolute inset-0 outline-none focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:ring-inset";

const content = "pointer-events-none relative flex items-center gap-2";

export type AppNodeData = {
  app: DeploymentApp;
  reads: number;
  ghost: boolean;
};

export type ResourceNodeData = {
  resource: DeploymentResource;
  provider: string;
  ghost: boolean;
};

export type GhostNodeData = { title: string; caption: string };

export type ServiceNode =
  | Node<AppNodeData, "app">
  | Node<ResourceNodeData, "resource">
  | Node<GhostNodeData, "ghost">;

const handleSide = "!size-0 !min-h-0 !min-w-0 !border-0 !bg-transparent !opacity-0";

export function AppNode({ id, data, selected }: NodeProps<Node<AppNodeData, "app">>) {
  const select = useContext(SelectNode);
  const { app, reads, ghost } = data;
  const url = app.urls[0];

  return (
    <>
      <Handle type="target" position={Position.Left} className={handleSide} isConnectable={false} />
      <div className={tileClass(selected === true, ghost, "bg-background")} style={APP_SIZE}>
        <button
          type="button"
          aria-label={`App ${app.name}, ${app.outcome}`}
          aria-pressed={selected === true}
          onClick={() => select(id)}
          className={surface}
        />
        <div className={content}>
          <span className={`flex shrink-0 ${ghost ? "opacity-50" : ""}`}>
            <AppMark app={app} />
          </span>
          <span className="min-w-0 flex-1 truncate text-sm font-semibold">{app.name}</span>
          {!ghost && <OutcomeDot outcome={app.outcome} />}
        </div>
        <div className="pointer-events-none relative flex items-center gap-1.5">
          <Chip>{app.compute}</Chip>
          <Chip>{app.runtime.name}</Chip>
        </div>
        {url ? (
          <a
            href={url}
            target="_blank"
            rel="noreferrer"
            className="pointer-events-auto relative flex min-w-0 items-center gap-1 font-mono text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
          >
            <span className="truncate">{url.replace(/^https?:\/\//, "")}</span>
            <ArrowSquareOutIcon aria-hidden className="size-3 shrink-0" />
          </a>
        ) : (
          <span className="pointer-events-none relative font-mono text-xs text-muted-foreground">
            no url
          </span>
        )}
        <span className={`pointer-events-none relative ${labelType}`}>
          {count(app.variables.length, "variable")} · {count(reads, "read")}
        </span>
      </div>
      <Handle
        type="source"
        position={Position.Right}
        className={handleSide}
        isConnectable={false}
      />
    </>
  );
}

export function ResourceNode({
  id,
  data,
  selected,
}: NodeProps<Node<ResourceNodeData, "resource">>) {
  const select = useContext(SelectNode);
  const { resource, provider, ghost } = data;
  const keys = resource.binding.propertyKeys.length;
  const grants = resource.binding.grants.length;

  return (
    <>
      <Handle type="target" position={Position.Left} className={handleSide} isConnectable={false} />
      <div className={tileClass(selected === true, ghost, "bg-muted")} style={RESOURCE_SIZE}>
        <button
          type="button"
          aria-label={`Resource ${resource.name}, ${resource.type}`}
          aria-pressed={selected === true}
          onClick={() => select(id)}
          className={surface}
        />
        <div className={content}>
          <span className={`flex shrink-0 ${ghost ? "opacity-50" : ""}`}>
            <ResourceMark resource={resource} />
          </span>
          <span className="min-w-0 flex-1 truncate text-sm font-semibold">{resource.name}</span>
          <ProviderMark provider={provider} resourceType={resource.type} />
        </div>
        <span className="pointer-events-none relative truncate font-mono text-xs text-muted-foreground">
          {resource.binding.name}
        </span>
        <span className={`pointer-events-none relative ${labelType}`}>
          {count(keys, "key")} · {count(grants, "grant")}
        </span>
      </div>
      <Handle
        type="source"
        position={Position.Right}
        className={handleSide}
        isConnectable={false}
      />
    </>
  );
}

export function GhostNode({ data }: NodeProps<Node<GhostNodeData, "ghost">>) {
  return (
    <>
      <Handle type="target" position={Position.Left} className={handleSide} isConnectable={false} />
      <div
        className="flex size-full flex-col justify-center gap-1 border border-dashed border-dim/60 bg-background p-3 text-muted-foreground"
        style={GHOST_SIZE}
      >
        <span className="text-sm font-semibold">{data.title}</span>
        <span className={labelType}>{data.caption}</span>
      </div>
      <Handle
        type="source"
        position={Position.Right}
        className={handleSide}
        isConnectable={false}
      />
    </>
  );
}

export const nodeTypes = { app: AppNode, resource: ResourceNode, ghost: GhostNode };
