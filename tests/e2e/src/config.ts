import { existsSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import stripJsonComments from "strip-json-comments";
import { appHostname } from "./identity";
import type {
  Cell,
  Compute,
  Edge,
  RegistryConfig,
  TargetConfigDelta,
  TargetName,
} from "./matrix/types";
import { REGISTRY_USER_ENV } from "./registry/settings";
import type { CellUnderTest } from "./run/cellRun";

type PlannedCell = Pick<CellUnderTest, "name" | "slug" | "fixture" | "variant">;

import { frontNamed } from "./targets/front";
import { gcpSlug } from "./targets/gcp/names";

export const JOURNEY_TS = "ocel.journey.config.ts";
export const JOURNEY_JSON = "ocel.journey.json";

export const JSON_BASE = "ocel.json";
export const PROGRAM_BASE = "ocel.config.ts";

export const BOX_ZONE = "localhost";

export function journeyZone(env: NodeJS.ProcessEnv): string {
  return env.OCEL_E2E_ZONE?.trim() || BOX_ZONE;
}

export function vpsZoneOf(cell: Pick<Cell, "variant">, env: NodeJS.ProcessEnv): string {
  return cell.variant.config.edge === "cloudflare" ? journeyZone(env) : BOX_ZONE;
}

export type Overlay = TargetConfigDelta & {
  target: TargetName;
  slug: string;
  compute?: Compute;
  computes?: Record<string, Compute>;
  edge?: Edge;
  tunnel?: boolean;
  dns?: "cloudflare";
  hostnames?: Record<string, string>;
  previewDomain?: string;
  variablesKey?: string;
  registry?: RegistryConfig;
  proxy?: unknown;
};

const EDGE_IMPORTS: Record<Edge, { name: string; from: string }> = {
  cloudfront: { name: "cloudfront", from: "ocel/providers/aws/edge" },
  "api-gateway": { name: "apiGateway", from: "ocel/providers/aws/edge" },
  cloudflare: { name: "cloudflare", from: "ocel/edge" },
  alb: { name: "alb", from: "ocel/providers/gcp/edge" },
};

export const HOSTNAME_EDGES: Edge[] = ["cloudflare", "alb"];

function hostnamesOf(cell: PlannedCell, zone: string): Record<string, string> {
  const named: Record<string, string> = {};
  for (const app of cell.fixture.apps) {
    const host = appHostname(app, cell.slug, zone);
    if (host) {
      named[app] = host;
    }
  }
  return named;
}

function registryOf(cell: PlannedCell, env: NodeJS.ProcessEnv): { registry?: RegistryConfig } {
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
  return { target: "aws", slug, ...cell?.variant?.config, ...dnsOf(env) };
}

export function overlayFor(cell: PlannedCell, target: TargetName, env: NodeJS.ProcessEnv): Overlay {
  const zone = env.OCEL_E2E_ZONE?.trim() || undefined;
  switch (target) {
    case "aws": {
      const variablesKey = env.OCEL_AWS_VARIABLES_KEY?.trim() || undefined;
      const { compute, computes } = cell.variant.config;
      const forwarded =
        cell.variant.config.edge === "cloudflare" &&
        (compute === "container" || Object.values(computes ?? {}).includes("container"));
      return {
        target,
        slug: cell.slug,
        ...cell.fixture.configOn?.aws,
        ...cell.variant.config,
        ...dnsOf(env),
        ...(forwarded && zone ? { dns: "cloudflare" as const } : {}),
        ...(zone ? { hostnames: hostnamesOf(cell, zone) } : {}),
        ...(variablesKey ? { variablesKey } : {}),
      };
    }
    case "gcp": {
      const { edge } = cell.variant.config;
      const hostnamed = edge !== undefined && HOSTNAME_EDGES.includes(edge) && zone;
      const previewing =
        edge === "alb" &&
        zone &&
        (cell.fixture.previews?.gcp ?? []).some((one) => one.name === cell.variant.name);
      return {
        target,
        slug: gcpSlug(cell, env),
        ...cell.fixture.configOn?.gcp,
        ...cell.variant.config,
        ...(hostnamed ? { dns: "cloudflare" as const, hostnames: hostnamesOf(cell, zone) } : {}),
        ...(previewing ? { previewDomain: `*.${appHostname("pv", cell.slug, zone)}` } : {}),
      };
    }
    case "vps": {
      const front = frontNamed(env);
      const { edge, tunnel } = cell.variant.config;
      return {
        target,
        slug: cell.slug,
        ...cell.fixture.configOn?.vps,
        hostnames: hostnamesOf(cell, vpsZoneOf(cell, env)),
        ...registryOf(cell, env),
        ...(front ? { proxy: front.proxy } : {}),
        ...(edge ? { edge } : {}),
        ...(tunnel ? { tunnel } : {}),
        ...(edge === "cloudflare" ? { dns: "cloudflare" as const } : {}),
      };
    }
    case "dev":
      return { target, slug: cell.slug, ...cell.fixture.configOn?.dev };
  }
}

const VPS_SSH_ENV = {
  host: "OCEL_VPS_HOST",
  user: "OCEL_VPS_USER",
  identityFile: "OCEL_VPS_IDENTITY_FILE",
};

const GCP_ENV = { project: "OCEL_GCP_PROJECT", region: "OCEL_GCP_REGION" };

const CONTAINER_HEALTH = { path: "/health" };

function buildEnvOf(name: string, vars: Record<string, string>): string {
  const fields = Object.values(vars).map((variable) => `  ${variable}: z.string().min(1),`);
  return `const ${name} = buildEnv({\n${fields.join("\n")}\n});\n`;
}

function readsOf(name: string, vars: Record<string, string>): string {
  const fields = Object.entries(vars).map(([key, variable]) => `${key}: ${name}.${variable}`);
  return `{ ${fields.join(", ")} }`;
}

function expressionOf(fields: Record<string, unknown>): string {
  const written = Object.entries(fields).map(
    ([key, value]) => `${key}: ${value === undefined ? "undefined" : JSON.stringify(value)}`,
  );
  return `{ ${written.join(", ")} }`;
}

function appOverlay(overlay: Overlay): string {
  const lines: string[] = [];
  if (overlay.target === "vps") {
    if (!overlay.compute) {
      lines.push(`    compute: "container",`);
    }
    lines.push(`    health: { path: ${JSON.stringify(CONTAINER_HEALTH.path)} },`);
  }
  for (const [app, fields] of Object.entries(overlay.apps ?? {})) {
    lines.push(`    ...(app.name === ${JSON.stringify(app)} ? ${expressionOf(fields)} : {}),`);
  }
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

export function renderConfig(base: string, overlay: Overlay): string {
  const imports: string[] = [];
  const env: string[] = [];
  const fields = [`  ...base,`, `  slug: ${JSON.stringify(overlay.slug)},`];
  if (overlay.target === "gcp") {
    imports.push(
      `import { buildEnv, defineConfig } from "ocel/config";`,
      `import gcpProvider from "ocel/providers/gcp";`,
      `import { z } from "zod";`,
    );
    env.push(buildEnvOf("gcp", GCP_ENV));
    fields.push(`  provider: gcpProvider(${readsOf("gcp", GCP_ENV)}),`);
  } else if (overlay.target === "vps") {
    imports.push(
      `import { buildEnv, defineConfig } from "ocel/config";`,
      `import vpsProvider from "ocel/providers/vps";`,
      `import { z } from "zod";`,
    );
    env.push(buildEnvOf("ssh", VPS_SSH_ENV));
    const proxy =
      overlay.proxy === undefined ? [] : [`    proxy: ${JSON.stringify(overlay.proxy)},`];
    fields.push(
      `  provider: vpsProvider({`,
      `    ssh: ${readsOf("ssh", VPS_SSH_ENV)},`,
      ...proxy,
      `  }),`,
    );
  } else {
    imports.push(`import { defineConfig } from "ocel/config";`);
    if (overlay.variablesKey) {
      fields.push(
        `  provider: { aws: { ...(base.provider !== null && typeof base.provider === "object" ? base.provider.aws : {}), variablesKey: ${JSON.stringify(overlay.variablesKey)} } },`,
      );
    }
  }
  if (overlay.edge) {
    const { name, from } = EDGE_IMPORTS[overlay.edge];
    imports.push(`import { ${name} } from ${JSON.stringify(from)};`);
  }
  if (overlay.dns) {
    imports.push(`import { cloudflareDns } from "ocel/dns";`);
  }
  imports.push(`import base from ${JSON.stringify(base)};`);

  if (overlay.allowDegraded) {
    fields.push(`  allowDegraded: ${JSON.stringify(overlay.allowDegraded)},`);
  }
  if (overlay.edge) {
    const options = overlay.tunnel ? "{ tunnel: true }" : "";
    fields.push(`  edge: ${EDGE_IMPORTS[overlay.edge].name}(${options}),`);
  }
  if (overlay.dns) {
    fields.push(`  dns: cloudflareDns(),`);
  }
  if (overlay.previewDomain) {
    fields.push(
      `  domains: { ...base.domains, preview: ${JSON.stringify(overlay.previewDomain)} },`,
    );
  }
  if (overlay.registry) {
    fields.push(`  registry: ${JSON.stringify(overlay.registry)},`);
  }
  const perApp = appOverlay(overlay);
  if (perApp !== "") {
    fields.push(`  apps: base.apps?.map((app) => ({`, `    ...app,`, perApp, `  })),`);
  }

  if (overlay.hostnames) {
    env.push(`const hostnames: Record<string, string> = ${JSON.stringify(overlay.hostnames)};\n`);
  }
  const preamble = env.map((one) => `\n${one}`).join("");

  return `${imports.join("\n")}\n${preamble}\nexport default defineConfig({\n${fields.join("\n")}\n});\n`;
}

type App = Record<string, unknown> & { name?: string };

type Document = Record<string, unknown> & {
  provider?: string | Record<string, Record<string, unknown>> | null;
  apps?: App[];
};

function appDocument(app: App, overlay: Overlay): App {
  const written: App = {
    ...app,
    ...(overlay.target === "vps" ? { compute: "container", health: CONTAINER_HEALTH } : {}),
    ...(app.name === undefined ? {} : overlay.apps?.[app.name]),
  };
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

function placeholdersOf(vars: Record<string, string>): Record<string, string> {
  return Object.fromEntries(
    Object.entries(vars).map(([key, variable]) => [key, `\${${variable}}`]),
  );
}

export function renderJsonConfig(base: string, overlay: Overlay): string {
  const read = JSON.parse(stripJsonComments(base)) as Document;
  const written: Document = { ...read, slug: overlay.slug };
  if (overlay.variablesKey) {
    const options =
      read.provider !== null && typeof read.provider === "object" ? read.provider.aws : undefined;
    written.provider = { aws: { ...options, variablesKey: overlay.variablesKey } };
  }
  if (overlay.target === "gcp") {
    written.provider = { gcp: placeholdersOf(GCP_ENV) };
  }
  if (overlay.target === "vps") {
    const proxy = overlay.proxy === undefined ? {} : { proxy: overlay.proxy };
    written.provider = { vps: { ssh: placeholdersOf(VPS_SSH_ENV), ...proxy } };
  }
  if (overlay.allowDegraded) {
    written.allowDegraded = overlay.allowDegraded;
  }
  if (overlay.edge) {
    written.edge = overlay.tunnel ? { [overlay.edge]: { tunnel: true } } : overlay.edge;
  }
  if (overlay.dns) {
    written.dns = overlay.dns;
  }
  if (overlay.previewDomain) {
    written.domains = { ...(read.domains as object | undefined), preview: overlay.previewDomain };
  }
  if (overlay.registry) {
    written.registry = overlay.registry;
  }
  if (read.apps) {
    written.apps = read.apps.map((app) => appDocument(app, overlay));
  }
  return `${JSON.stringify(written, null, 2)}\n`;
}

function baseIn(dir: string): string {
  return existsSync(path.join(dir, JSON_BASE)) || !existsSync(path.join(dir, PROGRAM_BASE))
    ? JSON_BASE
    : PROGRAM_BASE;
}

export function journeyConfigIn(dir: string): string {
  return existsSync(path.join(dir, JOURNEY_JSON)) ? JOURNEY_JSON : JOURNEY_TS;
}

export async function writeJourneyConfig(dir: string, overlay: Overlay): Promise<string> {
  const base = baseIn(dir);
  if (base === PROGRAM_BASE) {
    const file = path.join(dir, JOURNEY_TS);
    await writeFile(file, renderConfig(`./${base}`, overlay), "utf8");
    return file;
  }
  const file = path.join(dir, JOURNEY_JSON);
  const read = await readFile(path.join(dir, base), "utf8");
  await writeFile(file, renderJsonConfig(read, overlay), "utf8");
  return file;
}
