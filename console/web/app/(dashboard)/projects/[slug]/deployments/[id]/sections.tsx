import type {
  Deployment,
  DeploymentApp,
  DeploymentResource,
  DeploymentStage,
} from "@console/db/schema";
import { ArrowSquareOutIcon } from "@phosphor-icons/react/dist/ssr";
import type { ReactNode } from "react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { spanOf } from "@/lib/runs";
import { labelType } from "@/lib/type";
import { cn } from "@/lib/utils";
import { CommandPane } from "../../../../command-pane";
import { AppMark, ProviderMark, ResourceMark } from "../../../../marks";
import { StatusDot } from "../cells";

export function Joined({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("grid grid-cols-1 pt-px pl-px", className)}>{children}</div>;
}

export function Cell({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn("-mt-px -ml-px min-w-0 border border-border p-4", className)}>
      {children}
    </div>
  );
}

export function Field({ name, children }: { name: string; children: ReactNode }) {
  return (
    <Cell className="flex flex-col gap-1">
      <dt className="text-xs text-muted-foreground">{name}</dt>
      <dd className="min-w-0 text-sm">{children}</dd>
    </Cell>
  );
}

export function NotReported() {
  return <span className="text-muted-foreground">Not reported</span>;
}

export function Panel({ heading, children }: { heading: string; children: ReactNode }) {
  return (
    <Cell className="flex flex-col gap-3">
      <h2 className={labelType}>{heading}</h2>
      {children}
    </Cell>
  );
}

const row =
  "flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border py-3 first:border-t-0 first:pt-0 last:pb-0";

export function AppList({ apps }: { apps: DeploymentApp[] }) {
  if (apps.length === 0) {
    return <p className="px-4 pb-4 text-sm text-muted-foreground">This run carried no apps.</p>;
  }
  return (
    <ul>
      {apps.map((app) => {
        const runtime = [app.compute, app.runtime.name, app.runtime.arch]
          .filter(Boolean)
          .join(" · ");
        return (
          <li
            key={app.name}
            className="border-t border-border py-3 first:border-t-0 first:pt-0 last:pb-0"
          >
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <AppMark app={app} size={18} />
              <span className="text-sm font-semibold">{app.name}</span>
              <span className="text-xs text-muted-foreground">{runtime}</span>
              {app.outcome !== "succeeded" && (
                <span className="inline-flex items-center gap-1.5 text-xs">
                  <StatusDot tone={app.outcome === "failed" ? "destructive" : "faint"} />
                  {app.outcome}
                </span>
              )}
              {app.urls[0] && (
                <a
                  href={app.urls[0]}
                  target="_blank"
                  rel="noreferrer"
                  className="ml-auto inline-flex items-center gap-1 font-mono text-xs underline-offset-4 outline-none hover:underline focus-visible:underline"
                >
                  {app.urls[0].replace(/^https?:\/\//, "")}
                  <ArrowSquareOutIcon aria-hidden className="size-3 text-muted-foreground" />
                </a>
              )}
            </div>
            {app.error && <p className="mt-2 font-mono text-xs text-destructive">{app.error}</p>}
          </li>
        );
      })}
    </ul>
  );
}

export function ResourceList({
  resources,
  readers,
  provider,
}: {
  resources: DeploymentResource[];
  readers: Map<string, string[]>;
  provider: string;
}) {
  if (resources.length === 0) {
    return (
      <p className="px-4 pb-4 text-sm text-muted-foreground">This run declared no resources.</p>
    );
  }
  return (
    <ul>
      {resources.map((resource) => {
        const apps = readers.get(resource.name) ?? [];
        return (
          <li key={resource.name} className={row}>
            <span className="relative">
              <ResourceMark resource={resource} size={18} />
              <span className="absolute -right-1 -bottom-1 bg-background">
                <ProviderMark provider={provider} resourceType={resource.type} size={10} />
              </span>
            </span>
            <span className="text-sm font-semibold">{resource.name}</span>
            <span className="text-xs text-muted-foreground">
              {resource.type} · {resource.binding.name}
            </span>
            <span className="ml-auto text-xs text-muted-foreground">
              {apps.length === 0 ? "Read by nothing" : `Read by ${apps.join(", ")}`}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

export function BuildLog({ stages }: { stages: DeploymentStage[] }) {
  if (stages.length === 0) {
    return <p className="p-4 text-sm text-muted-foreground">This run reported no build log.</p>;
  }
  return (
    <pre className="max-h-[32rem] overflow-auto bg-terminal px-4 py-3 font-mono text-xs leading-5 text-terminal-foreground">
      {stages.map((stage) => (
        <span key={`${stage.name}-${stage.app ?? ""}-${stage.startedAt}`} className="block">
          <span className="block text-terminal-foreground/60">
            # {stage.name}
            {stage.app ? ` · ${stage.app}` : ""} ·{" "}
            {spanOf(Date.parse(stage.startedAt), Date.parse(stage.finishedAt))}
            {stage.status === "skipped" ? " · skipped" : ""}
          </span>
          {stage.log.map((line, at) => (
            <span key={at} className="block text-terminal-ink">
              {line}
            </span>
          ))}
          {stage.error && <span className="block text-destructive">{stage.error}</span>}
        </span>
      ))}
    </pre>
  );
}

function Planned() {
  return (
    <span className="text-muted-foreground" title="Arrives through a connector in your account">
      Via connector
    </span>
  );
}

function Head({ labels }: { labels: string[] }) {
  return (
    <TableHeader>
      <TableRow className="hover:bg-transparent">
        {labels.map((label) => (
          <TableHead key={label} className={`h-9 px-4 ${labelType}`}>
            {label}
          </TableHead>
        ))}
      </TableRow>
    </TableHeader>
  );
}

const cell = "h-10 px-4 text-sm";

export function hostnamesOf(apps: DeploymentApp[]) {
  return apps.flatMap((app) => app.hostnames.map((hostname) => ({ app: app.name, hostname })));
}

export function Domains({ apps }: { apps: DeploymentApp[] }) {
  const rows = hostnamesOf(apps);
  if (rows.length === 0) {
    return (
      <div className="flex flex-col items-start gap-3 p-4">
        <p className="text-sm text-muted-foreground">
          No hostnames declared. Apps answer on the urls their provider assigned.
        </p>
        <CommandPane command="ocel domain add" />
      </div>
    );
  }
  return (
    <Table className="min-w-[32rem]">
      <Head labels={["Hostname", "App", "DNS", "Certificate"]} />
      <TableBody>
        {rows.map((entry) => (
          <TableRow key={`${entry.app}-${entry.hostname}`} className="hover:bg-transparent">
            <TableCell className={`${cell} font-mono text-xs`}>{entry.hostname}</TableCell>
            <TableCell className={cell}>{entry.app}</TableCell>
            <TableCell className={cell}>
              <Planned />
            </TableCell>
            <TableCell className={cell}>
              <Planned />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export function checksOf(apps: DeploymentApp[]) {
  return apps.filter((app) => app.healthPath);
}

export function Checks({ apps }: { apps: DeploymentApp[] }) {
  const rows = checksOf(apps);
  if (rows.length === 0) {
    return <p className="p-4 text-sm text-muted-foreground">No health check paths declared.</p>;
  }
  return (
    <Table className="min-w-[32rem]">
      <Head labels={["App", "Path", "Status"]} />
      <TableBody>
        {rows.map((app) => (
          <TableRow key={app.name} className="hover:bg-transparent">
            <TableCell className={cell}>{app.name}</TableCell>
            <TableCell className={`${cell} font-mono text-xs`}>{app.healthPath}</TableCell>
            <TableCell className={cell}>
              <Planned />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export function readersOf(run: Deployment): Map<string, string[]> {
  const readers = new Map<string, string[]>();
  for (const usage of run.topology.usages) {
    readers.set(usage.resource, [...(readers.get(usage.resource) ?? []), usage.app]);
  }
  return readers;
}
