"use client";

import type { DeploymentApp, DeploymentResource, Framework } from "@console/db/schema";
import { CloudIcon, CubeIcon, HardDrivesIcon, PackageIcon } from "@phosphor-icons/react";
import {
  type SimpleIcon,
  siBun,
  siCloudflare,
  siDeno,
  siDigitalocean,
  siGo,
  siHetzner,
  siNextdotjs,
  siNodedotjs,
  siPostgresql,
  siPython,
  siRust,
} from "simple-icons";
import { RdsIcon, S3Icon } from "@/components/marks/aws";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { frameworkCatalog } from "@/lib/frameworks";

const runtimeMarks: Record<string, SimpleIcon> = {
  node: siNodedotjs,
  nodejs: siNodedotjs,
  next: siNextdotjs,
  nextjs: siNextdotjs,
  go: siGo,
  golang: siGo,
  python: siPython,
  bun: siBun,
  deno: siDeno,
  rust: siRust,
};

const providerMarks: Record<string, SimpleIcon> = {
  cloudflare: siCloudflare,
  digitalocean: siDigitalocean,
  hetzner: siHetzner,
};

function Glyph({ icon, size = 20 }: { icon: SimpleIcon; size?: number }) {
  return (
    <svg
      aria-hidden
      viewBox="0 0 24 24"
      fill={`#${icon.hex}`}
      style={{ width: size, height: size }}
      className="shrink-0"
    >
      <title>{icon.title}</title>
      <path d={icon.path} />
    </svg>
  );
}

function FrameworkMark({ framework, size }: { framework: Framework; size: number }) {
  const entry = frameworkCatalog[framework];
  if (!entry.logo) {
    return <Glyph icon={entry.icon} size={size} />;
  }
  return (
    <span className="shrink-0" style={{ width: size, height: size }}>
      {/** biome-ignore lint/performance/noImgElement: brand marks are static svg files */}
      <img
        src={entry.logo.light}
        alt=""
        className={`size-full object-contain ${entry.logo.dark ? "dark:hidden" : ""}`}
      />
      {entry.logo.dark && (
        /** biome-ignore lint/performance/noImgElement: brand marks are static svg files */
        <img src={entry.logo.dark} alt="" className="hidden size-full object-contain dark:block" />
      )}
    </span>
  );
}

export function AppMark({ app, size = 20 }: { app: DeploymentApp; size?: number }) {
  if (app.framework) {
    return <FrameworkMark framework={app.framework} size={size} />;
  }
  const mark = runtimeMarks[app.runtime.name.toLowerCase()];
  if (mark) {
    return <Glyph icon={mark} size={size} />;
  }
  return (
    <CubeIcon
      aria-hidden
      className="shrink-0 text-muted-foreground"
      style={{ width: size, height: size }}
    />
  );
}

const awsProviderMarks: Record<string, typeof S3Icon> = { postgres: RdsIcon, bucket: S3Icon };

export function ResourceMark({
  resource,
  size = 20,
}: {
  resource: DeploymentResource;
  size?: number;
}) {
  if (resource.type === "postgres") {
    return <Glyph icon={siPostgresql} size={size} />;
  }
  const Icon = resource.type === "bucket" ? PackageIcon : CubeIcon;
  return (
    <Icon
      aria-hidden
      className="shrink-0 text-muted-foreground"
      style={{ width: size, height: size }}
    />
  );
}

export function ProviderMark({
  provider,
  resourceType,
  size = 14,
}: {
  provider: string;
  resourceType?: string;
  size?: number;
}) {
  const style = { width: size, height: size };
  const name = provider.toLowerCase();

  if (name === "aws") {
    const Aws = resourceType ? awsProviderMarks[resourceType] : undefined;
    if (Aws) {
      return <Aws className="shrink-0 text-muted-foreground" style={style} />;
    }
    return (
      /** biome-ignore lint/performance/noImgElement: brand marks are static svg files */
      <img src="/providers/aws.svg" alt="" className="shrink-0 object-contain" style={style} />
    );
  }

  const mark = providerMarks[name];
  if (mark) {
    return <Glyph icon={mark} size={size} />;
  }
  const Icon = name === "vps" ? HardDrivesIcon : CloudIcon;
  return <Icon aria-hidden className="shrink-0 text-muted-foreground" style={style} />;
}

const outcomeTone: Record<DeploymentApp["outcome"], string> = {
  succeeded: "bg-go",
  failed: "bg-destructive",
  skipped: "bg-faint",
};

export function OutcomeDot({ outcome }: { outcome: DeploymentApp["outcome"] }) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            role="img"
            aria-label={outcome}
            className={`pointer-events-auto size-2 rounded-full ${outcomeTone[outcome]}`}
          />
        }
      />
      <TooltipContent>{outcome}</TooltipContent>
    </Tooltip>
  );
}

export function EdgeMark({ edge, size = 14 }: { edge: string; size?: number }) {
  const mark = providerMarks[edge.toLowerCase()];
  if (mark) {
    return <Glyph icon={mark} size={size} />;
  }
  return (
    <CloudIcon
      aria-hidden
      className="shrink-0 text-muted-foreground"
      style={{ width: size, height: size }}
    />
  );
}
