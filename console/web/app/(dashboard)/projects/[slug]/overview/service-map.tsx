"use client";

import type { DeploymentTopology } from "@console/db/schema";
import dagre from "@dagrejs/dagre";
import {
  Background,
  BackgroundVariant,
  type Edge,
  EdgeLabelRenderer,
  type EdgeProps,
  getBezierPath,
  ReactFlow,
  ReactFlowProvider,
  useEdgesState,
  useNodesState,
  useReactFlow,
} from "@xyflow/react";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { TooltipProvider } from "@/components/ui/tooltip";
import "@xyflow/react/dist/style.css";
import { DetailsPanel, type Selection } from "./details-panel";
import {
  APP_SIZE,
  nodeTypes,
  RESOURCE_SIZE,
  SelectNodeProvider,
  type ServiceNode,
  useFitOptions,
} from "./nodes";
import { type Failure, type Provenance, ProvenanceStrip, ViewControls } from "./provenance";

type UsageEdgeData = { app: string; resource: string; files: string[] };

const ActiveEdges = createContext<ReadonlySet<string>>(new Set());

const HoveredEdge = createContext<string | null>(null);

function UsageEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
}: EdgeProps<Edge<UsageEdgeData>>) {
  const active = useContext(ActiveEdges).has(id);
  const captioned = useContext(HoveredEdge) === id;
  const [path, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
  });

  return (
    <>
      <path
        d={path}
        fill="none"
        strokeWidth={1.5}
        strokeDasharray="5 5"
        className={active ? "stroke-foreground" : "stroke-dim opacity-60"}
      />
      <path
        d={path}
        fill="none"
        strokeWidth={16}
        stroke="transparent"
        className="pointer-events-auto"
      />
      {captioned && data && data.files.length > 0 && (
        <EdgeLabelRenderer>
          <div
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
            className="pointer-events-none absolute z-20 max-w-[160px] border border-border bg-background px-2 py-1"
          >
            <ul className="flex flex-col gap-0.5">
              {data.files.map((file) => (
                <li key={file} className="break-all font-mono text-xs text-muted-foreground">
                  {file}
                </li>
              ))}
            </ul>
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  );
}

const edgeTypes = { usage: UsageEdge };

const appId = (name: string) => `app:${name}`;
const resourceId = (name: string) => `resource:${name}`;

function laidOut(
  topology: DeploymentTopology,
  provider: string,
  ghost: boolean,
): { nodes: ServiceNode[]; edges: Edge<UsageEdgeData>[] } {
  const graph = new dagre.graphlib.Graph();
  graph.setGraph({ rankdir: "LR", ranksep: 176, nodesep: 24, marginx: 16, marginy: 16 });
  graph.setDefaultEdgeLabel(() => ({}));

  for (const app of topology.apps) {
    graph.setNode(appId(app.name), { ...APP_SIZE });
  }
  for (const resource of topology.resources) {
    graph.setNode(resourceId(resource.name), { ...RESOURCE_SIZE });
  }

  const usages = topology.usages.filter(
    (usage) =>
      topology.apps.some((app) => app.name === usage.app) &&
      topology.resources.some((resource) => resource.name === usage.resource),
  );
  for (const usage of usages) {
    graph.setEdge(appId(usage.app), resourceId(usage.resource));
  }

  dagre.layout(graph);

  const place = (id: string, size: { width: number; height: number }) => {
    const node = graph.node(id);
    return { x: (node?.x ?? 0) - size.width / 2, y: (node?.y ?? 0) - size.height / 2 };
  };

  const nodes: ServiceNode[] = [
    ...topology.apps.map(
      (app): ServiceNode => ({
        id: appId(app.name),
        type: "app",
        position: place(appId(app.name), APP_SIZE),
        ...APP_SIZE,
        ariaLabel: `App ${app.name}`,
        data: {
          app,
          ghost,
          reads: usages.filter((usage) => usage.app === app.name).length,
        },
      }),
    ),
    ...topology.resources.map(
      (resource): ServiceNode => ({
        id: resourceId(resource.name),
        type: "resource",
        position: place(resourceId(resource.name), RESOURCE_SIZE),
        ...RESOURCE_SIZE,
        ariaLabel: `Resource ${resource.name}`,
        data: { resource, provider, ghost },
      }),
    ),
  ];

  const edges = usages.map(
    (usage): Edge<UsageEdgeData> => ({
      id: `${usage.app}->${usage.resource}`,
      type: "usage",
      source: appId(usage.app),
      target: resourceId(usage.resource),
      ariaLabel: `${usage.app} reads ${usage.resource}`,
      data: { app: usage.app, resource: usage.resource, files: usage.files },
    }),
  );

  return { nodes, edges };
}

function Canvas({
  topology,
  provenance,
  failure,
  now,
  ghost,
  stampPrefix,
}: {
  topology: DeploymentTopology;
  provenance?: Provenance;
  failure?: Failure;
  now: string;
  ghost: boolean;
  stampPrefix?: string;
}) {
  const { fitView } = useReactFlow();
  const fitOptions = useFitOptions();
  const frame = useRef<HTMLElement>(null);
  const [hovered, setHovered] = useState<string | null>(null);

  const initial = useMemo(
    () => laidOut(topology, provenance?.providerName ?? "", ghost),
    [topology, provenance?.providerName, ghost],
  );

  const [nodes, setNodes, onNodesChange] = useNodesState(initial.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(initial.edges);

  const select = useCallback(
    (id: string) => {
      setHovered(null);
      setNodes((current) => current.map((node) => ({ ...node, selected: node.id === id })));
    },
    [setNodes],
  );

  useEffect(() => {
    setNodes(initial.nodes);
    setEdges(initial.edges);
  }, [initial, setNodes, setEdges]);

  const selectedId = useMemo(() => nodes.find((node) => node.selected)?.id ?? null, [nodes]);

  const selection = useMemo((): Selection | null => {
    if (!selectedId) {
      return null;
    }
    const [kind, ...rest] = selectedId.split(":");
    return { kind: kind === "app" ? "app" : "resource", name: rest.join(":") };
  }, [selectedId]);

  const clear = useCallback(() => {
    setHovered(null);
    setNodes((current) =>
      current.some((node) => node.selected)
        ? current.map((node) => ({ ...node, selected: false }))
        : current,
    );
  }, [setNodes]);

  const refit = useCallback(() => {
    fitView({ ...fitOptions, duration: 200 });
  }, [fitView, fitOptions]);

  useEffect(() => {
    const element = frame.current;
    if (!element) {
      return;
    }
    let timer: ReturnType<typeof setTimeout>;
    const schedule = () => {
      clearTimeout(timer);
      timer = setTimeout(refit, 80);
    };
    schedule();
    const observer = new ResizeObserver(schedule);
    observer.observe(element);
    return () => {
      clearTimeout(timer);
      observer.disconnect();
    };
  }, [refit]);

  const active = useMemo(
    () =>
      new Set(
        edges
          .filter(
            (edge) =>
              edge.id === hovered || edge.source === selectedId || edge.target === selectedId,
          )
          .map((edge) => edge.id),
      ),
    [edges, hovered, selectedId],
  );

  return (
    <TooltipProvider>
      <div className="flex size-full min-h-0">
        <section
          ref={frame}
          data-service-map
          aria-label="Service map"
          className="relative min-w-0 flex-1"
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              clear();
            }
          }}
        >
          <ActiveEdges.Provider value={active}>
            <HoveredEdge.Provider value={hovered}>
              <SelectNodeProvider value={select}>
                <ReactFlow
                  nodes={nodes}
                  edges={edges}
                  onNodesChange={onNodesChange}
                  onEdgesChange={onEdgesChange}
                  nodeTypes={nodeTypes}
                  edgeTypes={edgeTypes}
                  onPaneClick={clear}
                  onEdgeMouseEnter={(_, edge) => setHovered(edge.id)}
                  onEdgeMouseLeave={() => setHovered(null)}
                  fitView
                  fitViewOptions={fitOptions}
                  panOnScroll
                  zoomOnScroll={false}
                  zoomOnPinch
                  zoomOnDoubleClick={false}
                  nodesDraggable
                  nodesConnectable={false}
                  elementsSelectable
                  minZoom={0.2}
                  maxZoom={1.5}
                  proOptions={{ hideAttribution: true }}
                >
                  <Background
                    variant={BackgroundVariant.Dots}
                    gap={24}
                    size={2}
                    color="color-mix(in srgb, var(--dim) 55%, transparent)"
                  />
                </ReactFlow>
              </SelectNodeProvider>
            </HoveredEdge.Provider>
          </ActiveEdges.Provider>
          <ProvenanceStrip
            provenance={provenance}
            failure={failure}
            now={now}
            prefix={stampPrefix}
          />
          <ViewControls />
        </section>
        {selection && (
          <DetailsPanel
            key={selectedId}
            selection={selection}
            topology={topology}
            provider={provenance?.providerName ?? ""}
            onClose={clear}
          />
        )}
      </div>
    </TooltipProvider>
  );
}

export function ServiceMap(props: {
  topology: DeploymentTopology;
  provenance?: Provenance;
  failure?: Failure;
  now: string;
  ghost?: boolean;
  stampPrefix?: string;
}) {
  return (
    <ReactFlowProvider>
      <Canvas {...props} ghost={props.ghost === true} />
    </ReactFlowProvider>
  );
}
