import type { DeploymentTopology } from "@console/db/schema";
import { Skeleton } from "@/components/ui/skeleton";
import type { Environment } from "@/lib/environment";
import { CommandPane } from "../../../command-pane";
import { labelType } from "../../../label";
import type { Failure, Provenance } from "./provenance";
import { ProvenanceStrip } from "./provenance";
import { ServiceMap } from "./service-map";

const ground = "relative size-full min-h-0 overflow-hidden bg-background";

const dots = {
  backgroundImage:
    "radial-gradient(color-mix(in srgb, var(--dim) 55%, transparent) 1px, transparent 1px)",
  backgroundSize: "24px 24px",
};

function GhostTile({
  title,
  caption,
  className,
}: {
  title: string;
  caption: string;
  className?: string;
}) {
  return (
    <div
      className={`flex w-50 flex-col gap-1 border border-dashed border-dim/60 bg-background p-3 text-muted-foreground ${className ?? ""}`}
    >
      <span className="text-sm font-semibold">{title}</span>
      <span className={labelType}>{caption}</span>
    </div>
  );
}

export function NeverDeployed({
  environment,
  failure,
  stamp,
  now,
}: {
  environment: Environment;
  failure?: Failure;
  stamp?: Provenance;
  now: string;
}) {
  const command = environment === "preview" ? "ocel preview up" : "ocel deploy";

  return (
    <div className={ground} style={dots}>
      <div className="absolute inset-0 grid place-items-center overflow-y-auto px-5 py-20">
        <div className="flex flex-col items-center gap-8">
          <div className="flex items-center gap-6">
            <GhostTile title="an app" caption="not deployed" />
            <svg aria-hidden viewBox="0 0 96 24" className="h-6 w-24 text-dim/60">
              <path
                d="M0 12 C 32 12, 64 12, 96 12"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.5"
                strokeDasharray="5 5"
              />
            </svg>
            <GhostTile title="a resource" caption="not deployed" />
          </div>
          <div className="flex max-w-lg flex-col items-start gap-5 border border-border bg-background px-5 py-8 md:px-10">
            <div className="flex flex-col gap-1">
              <h1 className="text-lg font-semibold tracking-tight">
                Nothing deployed to {environment} yet
              </h1>
              <p className="text-muted-foreground">
                Deploys run from your terminal and land in your own cloud. Run this in the
                project&rsquo;s directory, and the console shows what it reported.
              </p>
            </div>
            <CommandPane command={command} />
          </div>
        </div>
      </div>
      <ProvenanceStrip
        provenance={stamp}
        failure={failure}
        now={now}
        prefix={stamp ? "Torn down" : undefined}
      />
    </div>
  );
}

export function TornDown({
  topology,
  provenance,
  now,
}: {
  topology: DeploymentTopology;
  provenance: Provenance;
  now: string;
}) {
  return (
    <ServiceMap
      topology={topology}
      provenance={provenance}
      now={now}
      ghost
      stampPrefix="Torn down"
    />
  );
}

export function LoadError({ href }: { href: string }) {
  return (
    <div className={ground} style={dots}>
      <div className="absolute inset-0 grid place-items-center px-5">
        <div className="flex max-w-lg flex-col items-start gap-5 border border-border bg-background px-5 py-8 md:px-10">
          <div className="flex flex-col gap-1" role="alert">
            <h1 className="text-lg font-semibold tracking-tight">Deployments didn&rsquo;t load</h1>
            <p className="text-muted-foreground">
              The console couldn&rsquo;t load this project&rsquo;s deployments. Nothing in your
              cloud account is affected.
            </p>
          </div>
          <a
            href={href}
            className="border border-border px-4 py-2.5 text-sm font-medium outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            Try again
          </a>
        </div>
      </div>
    </div>
  );
}

const skeletons = [
  { top: "18%", left: "12%", width: 264, height: 112 },
  { top: "48%", left: "12%", width: 264, height: 112 },
  { top: "24%", left: "58%", width: 232, height: 92 },
  { top: "56%", left: "58%", width: 232, height: 92 },
];

export function MapSkeleton() {
  return (
    <div className={ground} style={dots}>
      <div className="absolute top-4 left-4 flex flex-col gap-2">
        <Skeleton className="h-8 w-28" />
        <Skeleton className="h-3 w-64" />
      </div>
      {skeletons.map((tile) => (
        <Skeleton
          key={`${tile.top}-${tile.left}`}
          className="absolute"
          style={{ top: tile.top, left: tile.left, width: tile.width, height: tile.height }}
        />
      ))}
    </div>
  );
}
