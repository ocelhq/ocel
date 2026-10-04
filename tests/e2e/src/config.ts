import { existsSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import stripJsonComments from "strip-json-comments";
import { appHostname } from "./identity";
import type { Cell, Compute, Edge, RegistryConfig, TargetName } from "./matrix/types";
import { REGISTRY_USER_ENV } from "./registry/settings";
import type { CellUnderTest } from "./run/cellRun";
import { frontNamed } from "./targets/front";
import { gcpSlug } from "./targets/gcp/names";

export const JOURNEY_TS = "ocel.journey.config.ts";
export const JOURNEY_JSON = "ocel.journey.json";

export const DEFAULT_BASE = "./ocel.json";
export const VPS_BASE = "./ocel.vps.json";
export const GCP_BASE = "./ocel.gcp.json";

export const BOX_ZONE = "localhost";

export function journeyZone(env: NodeJS.ProcessEnv): string {
  return env.OCEL_E2E_ZONE?.trim() || BOX_ZONE;
}

export function vpsZoneOf(cell: Pick<Cell, "variant">, env: NodeJS.ProcessEnv): string {
  return cell.variant.config.edge === "cloudflare" ? journeyZone(env) : BOX_ZONE;
}

export type Overlay = {
  base: string;
  slug: string;
  compute?: Compute;
  computes?: Record<string, Compute>;
  edge?: Edge;
  tunnel?: boolean;
  dns?: "cloudflare";
  hostnames?: Record<string, string>;
  variablesKey?: string;
  registry?: RegistryConfig;
  proxy?: unknown;
};

const EDGE_IMPORTS: Record<Edge, { name: string; from: string }> = {
  cloudfront: { name: "cloudfront", from: "ocel/providers/aws/edge" },
  "api-gateway": { name: "apiGateway", from: "ocel/providers/aws/edge" },
  cloudflare: { name: "cloudflare", from: "ocel/edge" },
};

function hostnamesOf(cell: CellUnderTest, zone: string): Record<string, string> {
  const named: Record<string, string> = {};
  for (const app of cell.fixture.apps) {
    const host = appHostname(app, cell.slug, zone);
    if (host) {
      named[app] = host;
    }
  }
  return named;
}

function registryOf(cell: CellUnderTest, env: NodeJS.ProcessEnv): { registry?: RegistryConfig } {
  const named = cell.variant.config.registry;
  if (!named) {
    return {};
  }
  const username = env[REGISTRY_USER_ENV]?.trim();
  if (!username) {
    throw new Error(
      `${cell.name} pushes to ${named.server} as the user ${REGISTRY_USER_ENV} names, and it is unset`,
    );
  }
  return { registry: { ...named, username } };
}

function dnsOf(env: NodeJS.ProcessEnv): { dns?: "cloudflare" } {
  return env.OCEL_E2E_DNS === "cloudflare" ? { dns: "cloudflare" } : {};
}

export function awsSweepOverlay(
  cell: Pick<Cell, "variant"> | undefined,
  slug: string,
  env: NodeJS.ProcessEnv,
): Overlay {
  return { base: DEFAULT_BASE, slug, ...cell?.variant?.config, ...dnsOf(env) };
}

export function overlayFor(
  cell: CellUnderTest,
  target: TargetName,
  env: NodeJS.ProcessEnv,
): Overlay {
  const zone = env.OCEL_E2E_ZONE?.trim() || undefined;
  switch (target) {
    case "aws": {
      const variablesKey = env.OCEL_AWS_VARIABLES_KEY?.trim() || undefined;
      const { compute, computes } = cell.variant.config;
      const forwarded =
        cell.variant.config.edge === "cloudflare" &&
        (compute === "container" || Object.values(computes ?? {}).includes("container"));
      return {
        base: DEFAULT_BASE,
        slug: cell.slug,
        ...cell.variant.config,
        ...dnsOf(env),
        ...(forwarded && zone ? { dns: "cloudflare" as const } : {}),
        ...(zone ? { hostnames: hostnamesOf(cell, zone) } : {}),
        ...(variablesKey ? { variablesKey } : {}),
      };
    }
    case "gcp": {
      const proxied = cell.variant.config.edge === "cloudflare" && zone;
      return {
        base: GCP_BASE,
        slug: gcpSlug(cell, env),
        ...cell.variant.config,
        ...(proxied ? { dns: "cloudflare" as const, hostnames: hostnamesOf(cell, zone) } : {}),
      };
    }
    case "vps": {
      const front = frontNamed(env);
      const { edge, tunnel } = cell.variant.config;
      return {
        base: VPS_BASE,
        slug: cell.slug,
        hostnames: hostnamesOf(cell, vpsZoneOf(cell, env)),
        ...registryOf(cell, env),
        ...(front ? { proxy: front.proxy } : {}),
        ...(edge ? { edge } : {}),
        ...(tunnel ? { tunnel } : {}),
        ...(edge === "cloudflare" ? { dns: "cloudflare" as const } : {}),
      };
    }
    case "dev":
      return { base: DEFAULT_BASE, slug: cell.slug };
  }
}

function appOverlay(overlay: Overlay): string {
  const lines: string[] = [];
  if (overlay.compute) {
    lines.push(`    compute: ${JSON.stringify(overlay.compute)},`);
  }
  if (overlay.compute === "container") {
    lines.push(`    framework: undefined,`);
    lines.push(`    arch: undefined,`);
  }
  for (const [app, compute] of Object.entries(overlay.computes ?? {})) {
    const unset = compute === "container" ? ", framework: undefined, arch: undefined" : "";
    lines.push(
      `    ...(app.name === ${JSON.stringify(app)} ? { compute: ${JSON.stringify(compute)}${unset} } : {}),`,
    );
  }
  if (overlay.hostnames) {
    lines.push(
      `    ...(hostnames[app.name] ? { domains: { production: hostnames[app.name] } } : {}),`,
    );
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
  if (overlay.variablesKey) {
    fields.push(
      `  provider: { aws: { ...(base.provider !== null && typeof base.provider === "object" ? base.provider.aws : {}), variablesKey: ${JSON.stringify(overlay.variablesKey)} } },`,
    );
  }
  if (overlay.proxy !== undefined) {
    fields.push(
      `  provider: { vps: { ...(base.provider !== null && typeof base.provider === "object" ? base.provider.vps : {}), proxy: ${JSON.stringify(overlay.proxy)} } },`,
    );
  }
  if (overlay.edge) {
    const options = overlay.tunnel ? "{ tunnel: true }" : "";
    fields.push(`  edge: ${EDGE_IMPORTS[overlay.edge].name}(${options}),`);
  }
  if (overlay.dns) {
    fields.push(`  dns: cloudflareDns(),`);
  }
  if (overlay.registry) {
    fields.push(`  registry: ${JSON.stringify(overlay.registry)},`);
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

type App = Record<string, unknown> & { name?: string };

type Document = Record<string, unknown> & {
  provider?: string | Record<string, Record<string, unknown>> | null;
  apps?: App[];
};

function appDocument(app: App, overlay: Overlay): App {
  const written: App = { ...app };
  const compute =
    (app.name === undefined ? undefined : overlay.computes?.[app.name]) ?? overlay.compute;
  if (compute) {
    written.compute = compute;
  }
  if (compute === "container") {
    delete written.framework;
    delete written.arch;
  }
  const hostname = app.name === undefined ? undefined : overlay.hostnames?.[app.name];
  if (hostname) {
    written.domains = { production: hostname };
  }
  return written;
}

export function renderJsonConfig(base: string, overlay: Overlay): string {
  const read = JSON.parse(stripJsonComments(base)) as Document;
  const written: Document = { ...read, slug: overlay.slug };
  if (overlay.variablesKey) {
    const options =
      read.provider !== null && typeof read.provider === "object" ? read.provider.aws : undefined;
    written.provider = { aws: { ...options, variablesKey: overlay.variablesKey } };
  }
  if (overlay.proxy !== undefined) {
    const options =
      read.provider !== null && typeof read.provider === "object" ? read.provider.vps : undefined;
    written.provider = { vps: { ...options, proxy: overlay.proxy } };
  }
  if (overlay.edge) {
    written.edge = overlay.tunnel ? { [overlay.edge]: { tunnel: true } } : overlay.edge;
  }
  if (overlay.dns) {
    written.dns = overlay.dns;
  }
  if (overlay.registry) {
    written.registry = overlay.registry;
  }
  if (read.apps) {
    written.apps = read.apps.map((app) => appDocument(app, overlay));
  }
  return `${JSON.stringify(written, null, 2)}\n`;
}

export function baseIn(dir: string, base: string): string {
  const asProgram = base.replace(/\.json$/, ".config.ts");
  if (
    asProgram !== base &&
    !existsSync(path.join(dir, base)) &&
    existsSync(path.join(dir, asProgram))
  ) {
    return asProgram;
  }
  return base;
}

export function journeyConfigIn(dir: string): string {
  return existsSync(path.join(dir, JOURNEY_JSON)) ? JOURNEY_JSON : JOURNEY_TS;
}

export async function writeJourneyConfig(dir: string, overlay: Overlay): Promise<string> {
  const base = baseIn(dir, overlay.base);
  if (!base.endsWith(".json")) {
    const file = path.join(dir, JOURNEY_TS);
    await writeFile(file, renderConfig({ ...overlay, base }), "utf8");
    return file;
  }
  const file = path.join(dir, JOURNEY_JSON);
  const read = await readFile(path.join(dir, base), "utf8");
  await writeFile(file, renderJsonConfig(read, { ...overlay, base }), "utf8");
  return file;
}
