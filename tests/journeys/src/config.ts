import { writeFile } from "node:fs/promises";
import path from "node:path";
import { appHostname } from "./identity";
import type { Compute, Edge, TargetName } from "./spec";
import type { CellContext } from "./targets/types";

export const JOURNEY_CONFIG = "ocel.journey.config.ts";

export const AWS_BASE = "./ocel.config.ts";
export const VPS_BASE = "./ocel.vps.config.ts";

const VPS_DEFAULT_ZONE = "localhost";

export function journeyZone(env: NodeJS.ProcessEnv): string {
  return env.OCEL_JOURNEY_ZONE?.trim() || VPS_DEFAULT_ZONE;
}

export type Overlay = {
  base: string;
  slug: string;
  compute?: Compute;
  edge?: Edge;
  dns?: "cloudflare";
  hostnames?: Record<string, string>;
};

const EDGE_IMPORTS: Record<Edge, { name: string; from: string }> = {
  cloudfront: { name: "cloudfront", from: "@ocel/provider-aws/edge" },
  "api-gateway": { name: "apiGateway", from: "@ocel/provider-aws/edge" },
  cloudflare: { name: "cloudflare", from: "ocel/edge" },
};

function hostnamesOf(cell: CellContext, zone: string): Record<string, string> {
  const named: Record<string, string> = {};
  for (const app of cell.example.apps) {
    const host = appHostname(app, cell.slug, zone);
    if (host) {
      named[app] = host;
    }
  }
  return named;
}

export function shapeFor(cell: CellContext, target: TargetName, env: NodeJS.ProcessEnv): Overlay {
  const zone = env.OCEL_JOURNEY_ZONE?.trim() || undefined;
  switch (target) {
    case "aws":
      return {
        base: AWS_BASE,
        slug: cell.slug,
        ...cell.variant?.config,
        ...(env.OCEL_JOURNEY_DNS === "cloudflare" ? { dns: "cloudflare" as const } : {}),
        ...(zone ? { hostnames: hostnamesOf(cell, zone) } : {}),
      };
    case "vps":
      return { base: VPS_BASE, slug: cell.slug, hostnames: hostnamesOf(cell, journeyZone(env)) };
    case "dev":
      return { base: AWS_BASE, slug: cell.slug };
  }
}

function appOverlay(overlay: Overlay): string {
  const lines: string[] = [];
  if (overlay.compute) {
    lines.push(`    compute: ${JSON.stringify(overlay.compute)},`);
  }
  if (overlay.hostnames) {
    lines.push(`    ...(hostnames[app.name] ? { domains: { production: hostnames[app.name] } } : {}),`);
  }
  return lines.join("\n");
}

export function renderConfig(overlay: Overlay): string {
  const imports = [`import { defineConfig } from "ocel/config";`];
  if (overlay.edge) {
    const { name, from } = EDGE_IMPORTS[overlay.edge];
    imports.push(`import { ${name} } from ${JSON.stringify(from)};`);
  }
  if (overlay.dns) {
    imports.push(`import { cloudflareDns } from "ocel/dns";`);
  }
  imports.push(`import base from ${JSON.stringify(overlay.base)};`);

  const fields = [`  ...base,`, `  slug: ${JSON.stringify(overlay.slug)},`];
  if (overlay.edge) {
    fields.push(`  edge: ${EDGE_IMPORTS[overlay.edge].name}(),`);
  }
  if (overlay.dns) {
    fields.push(`  dns: cloudflareDns(),`);
  }
  const perApp = appOverlay(overlay);
  if (perApp !== "") {
    fields.push(`  apps: base.apps?.map((app) => ({`, `    ...app,`, perApp, `  })),`);
  }

  const hostnames = overlay.hostnames
    ? `\nconst hostnames: Record<string, string> = ${JSON.stringify(overlay.hostnames)};\n`
    : "";

  return `${imports.join("\n")}\n${hostnames}\nexport default defineConfig({\n${fields.join("\n")}\n});\n`;
}

export async function writeJourneyConfig(dir: string, overlay: Overlay): Promise<string> {
  const file = path.join(dir, JOURNEY_CONFIG);
  await writeFile(file, renderConfig(overlay), "utf8");
  return file;
}
