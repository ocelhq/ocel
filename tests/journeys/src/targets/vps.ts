import { execFile } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { promisify } from "node:util";
import { HARNESS_ONLY_ENV } from "@ocel-tests/shared/env";
import { INITIAL_GREETING, redact, REDACTED, SECRET_TOKEN } from "../contract";
import type { ExpectationEnvironment } from "../expectations/types";
import { appHostname, HARNESS_PREFIX } from "../identity";
import { exitedBadly, ocel, runOcel, spawnOcel, workTree } from "../ocel";
import { outputRoot } from "../paths";
import type { Leg } from "../spec";
import { type Gateway, openGateway } from "./gateway";
import type { CellContext, Deployment, Target } from "./types";

const DEFAULT_ZONE = "localhost";
const CONFIG = "ocel.vps.config.ts";
const DEPLOY_LOGIN = "ocel-deploy";
const INCUS_MARKER = "/dev/virtio-ports/org.linuxcontainers.incus";
const PROJECT_RECORDS = "/var/lib/ocel/production/records/projects/production";
const PROXY_ROOT = "/var/lib/ocel/proxy/data/caddy/pki/authorities/local/root.crt";
const NO_RECORDS_TIER = "no-records-tier";
const ROOT_WAIT_MS = 180_000;
const ROOT_FIRST_WAIT_MS = 500;
const ROOT_LONGEST_WAIT_MS = 5_000;

const BRING_A_BOX_UP = [
  "scripts/incus.sh create <name>",
  'eval "$(scripts/incus.sh info <name>)"',
  "export OCEL_VPS_HOST=$OCEL_INCUS_ADDR OCEL_VPS_USER=$OCEL_INCUS_USER OCEL_VPS_IDENTITY_FILE=$OCEL_INCUS_KEY",
].join("\n  ");

export type Box = { host: string; user: string; identityFile: string };

type Standing = { dir: string; gateway: Gateway; env: NodeJS.ProcessEnv };

const standing = new Map<string, Standing>();

let bootstrapped: Promise<void> | undefined;

let resolvedBox: Box | undefined;

let resolvedZone: string | undefined;

const ran = promisify(execFile);

export function boxEnvironment(said: string): ExpectationEnvironment {
  const verdict = said.trim();
  if (verdict === "incus") {
    return "vps.incus";
  }
  if (verdict === "real") {
    return "vps";
  }
  throw new Error(
    `the box answered ${JSON.stringify(verdict)} when asked whether it runs under incus`,
  );
}

export function journeyZone(env: NodeJS.ProcessEnv): string {
  return env.OCEL_JOURNEY_ZONE?.trim() || DEFAULT_ZONE;
}

export function issuedByTheBox(zone: string): boolean {
  return zone === DEFAULT_ZONE || zone.endsWith(`.${DEFAULT_ZONE}`);
}

export function recordFile(slug: string): string {
  const encoded = Array.from(Buffer.from(slug, "utf8"), (byte, at) => {
    const char = String.fromCharCode(byte);
    const plain = /^[A-Za-z0-9\-_.]$/.test(char) && !(at === 0 && char === ".");
    return plain ? char : `%${byte.toString(16).toUpperCase().padStart(2, "0")}`;
  }).join("");
  return `${encoded}.rec`;
}

export function slugsOf(listing: string): string[] {
  if (listing.trim() === NO_RECORDS_TIER) {
    throw new Error(
      `${PROJECT_RECORDS} does not exist on the box, so nothing here can tell a box that holds ` +
        "no harness project from one this listing failed to read",
    );
  }
  return listing
    .split("\n")
    .map((line) => line.trim().split("/").pop() ?? "")
    .filter((name) => name.endsWith(".rec"))
    .map((name) => decodeURIComponent(name.slice(0, -".rec".length)));
}

export function strandedSlugs(slugs: string[], runId: string): string[] {
  const mine = `${HARNESS_PREFIX}${runId}-`;
  return slugs.filter((slug) => slug.startsWith(HARNESS_PREFIX) && !slug.startsWith(mine));
}

export function sshRefusal(target: Box, login: string, command: string, error: unknown): Error {
  const said = error as { code?: number | string; stderr?: unknown };
  const shown = (text: string) => redact(text).split(target.identityFile).join(REDACTED);
  const status = said?.code === undefined ? "without a status" : `${said.code}`;
  const stderr = typeof said?.stderr === "string" ? shown(said.stderr).trim() : "";
  return new Error(
    `ssh ${login}@${target.host} exited ${status} running ${shown(command)}` +
      `${stderr ? `\n${stderr}` : ""}`,
  );
}

function readBox(): Box {
  const host = process.env.OCEL_VPS_HOST;
  const user = process.env.OCEL_VPS_USER;
  const identityFile = process.env.OCEL_VPS_IDENTITY_FILE;
  if (!host || !user || !identityFile) {
    throw new Error(
      "OCEL_VPS_HOST, OCEL_VPS_USER and OCEL_VPS_IDENTITY_FILE name the box this target deploys to, " +
        `and the journey harness never brings one up. Run:\n  ${BRING_A_BOX_UP}`,
    );
  }
  return { host, user, identityFile };
}

function box(): Box {
  resolvedBox ??= readBox();
  return resolvedBox;
}

function zone(): string {
  resolvedZone ??= journeyZone(process.env);
  return resolvedZone;
}

export async function ssh(target: Box, login: string, command: string): Promise<string> {
  try {
    const { stdout } = await ran("ssh", [
      "-i",
      target.identityFile,
      "-o",
      "IdentitiesOnly=yes",
      "-o",
      "BatchMode=yes",
      "-o",
      "StrictHostKeyChecking=accept-new",
      "-o",
      "ConnectTimeout=10",
      `${login}@${target.host}`,
      command,
    ]);
    return stdout;
  } catch (error) {
    throw sshRefusal(target, login, command, error);
  }
}

async function guard(): Promise<ExpectationEnvironment> {
  const target = box();
  let said: string;
  try {
    said = await ssh(target, target.user, `test -e ${INCUS_MARKER} && echo incus || echo real`);
  } catch (error) {
    throw new Error(
      `${target.user}@${target.host} does not answer over ssh, and the journey harness never ` +
        `brings a box up. Run:\n  ${BRING_A_BOX_UP}\n\n${(error as Error).message}`,
    );
  }
  return boxEnvironment(said);
}

function boxEnv(login: string): NodeJS.ProcessEnv {
  const target = box();
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    OCEL_VPS_HOST: target.host,
    OCEL_VPS_USER: login,
    OCEL_VPS_IDENTITY_FILE: target.identityFile,
  };
  for (const name of HARNESS_ONLY_ENV) {
    delete env[name];
  }
  return env;
}

function childEnv(cell: CellContext, login: string): NodeJS.ProcessEnv {
  return { ...boxEnv(login), OCEL_JOURNEY_RUN: cell.runId, OCEL_JOURNEY_ZONE: zone() };
}

async function boxConfig(dir: string, slug: string, login: string): Promise<string> {
  const target = box();
  await mkdir(dir, { recursive: true });
  await writeFile(
    path.join(dir, "ocel.config.ts"),
    `import vps from "@ocel/provider-vps";
import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: ${JSON.stringify(slug)},
  provider: vps({
    ssh: {
      host: ${JSON.stringify(target.host)},
      user: ${JSON.stringify(login)},
      identityFile: ${JSON.stringify(target.identityFile)},
    },
  }),
  apps: [],
});
`,
    "utf8",
  );
  return dir;
}

async function setup(): Promise<void> {
  await guard();
  const target = box();
  bootstrapped ??= (async () => {
    const dir = await boxConfig(
      path.join(outputRoot, "vps", "box"),
      `${HARNESS_PREFIX}journey-bootstrap`,
      target.user,
    );
    const args = ["bootstrap", "production", "--yes"];
    const result = await spawnOcel(dir, args, boxEnv(target.user));
    const log = redact(`${result.stdout}${result.stderr}`);
    await writeFile(path.join(dir, "bootstrap.log"), log, "utf8");
    if (result.code !== 0) {
      throw exitedBadly(args, result);
    }
  })();
  await bootstrapped;
}

async function trusted(cell: CellContext): Promise<string | undefined> {
  if (!issuedByTheBox(zone())) {
    return undefined;
  }
  const target = box();
  const deadline = Date.now() + ROOT_WAIT_MS;
  let wait = ROOT_FIRST_WAIT_MS;
  while (true) {
    const root = await ssh(target, target.user, `sudo cat ${PROXY_ROOT} 2>/dev/null || true`);
    if (root.includes("BEGIN CERTIFICATE")) {
      await cell.evidence.write("up", "proxy-root.pem", root);
      return path.join(cell.evidence.dir, "up", "proxy-root.pem");
    }
    if (Date.now() >= deadline) {
      throw new Error(
        `${PROXY_ROOT} holds no issuing root ${ROOT_WAIT_MS / 1000}s after the deploy, and every ` +
          `hostname on ${zone()} is settled against a certificate this box issues itself`,
      );
    }
    await delay(wait);
    wait = Math.min(wait * 2, ROOT_LONGEST_WAIT_MS);
  }
}

async function bindDomains(cell: CellContext, started: Standing): Promise<void> {
  const root = await trusted(cell);
  await runOcel(cell, started.dir, "up", "domain-add", ["--config", CONFIG, "domain", "add"], {
    ...started.env,
    HTTPS_PROXY: started.gateway.tunnelUrl,
    ...(root ? { SSL_CERT_FILE: root } : {}),
  });
}

async function deployment(cell: CellContext, started: Standing): Promise<Deployment> {
  const urls = new Map<string, string>();
  for (const app of cell.example.apps) {
    const hostname = appHostname(app, cell.slug, zone());
    if (!hostname) {
      throw new Error(`${app} has no hostname on ${zone()}`);
    }
    urls.set(app, await started.gateway.serving(hostname));
  }
  await cell.evidence.write(
    "up",
    "deployment.json",
    `${JSON.stringify({ slug: cell.slug, zone: zone(), apps: Object.fromEntries(urls) }, null, 2)}\n`,
  );
  return {
    baseUrl: (app) => {
      const url = urls.get(app);
      if (!url) {
        throw new Error(`${cell.example.name} has no app named ${app} on vps`);
      }
      return url;
    },
    fetch: (...args) => fetch(...args),
  };
}

async function standingFor(cell: CellContext): Promise<Standing> {
  const already = standing.get(cell.slug);
  if (already) {
    return already;
  }
  const started: Standing = {
    dir: await workTree(cell, "vps"),
    gateway: await openGateway(box().host),
    env: childEnv(cell, DEPLOY_LOGIN),
  };
  standing.set(cell.slug, started);
  return started;
}

function driving(cell: CellContext, started: Standing, leg: Leg) {
  return (name: string, args: string[]) =>
    runOcel(cell, started.dir, leg, name, ["--config", CONFIG, ...args], started.env);
}

async function up(cell: CellContext): Promise<Deployment> {
  const started = await standingFor(cell);
  const drive = driving(cell, started, "up");

  await drive("env-greeting", ["env", "set", "GREETING", INITIAL_GREETING]);
  await drive("env-secret", ["env", "set", "SECRET_TOKEN", SECRET_TOKEN]);
  await drive("deploy", ["deploy", "--yes"]);
  await bindDomains(cell, started);
  if (cell.example.suites.includes("product")) {
    await drive("migrate", ["run", "--", "pnpm", "migrate"]);
  }
  return deployment(cell, started);
}

async function redeploy(cell: CellContext, greeting: string): Promise<Deployment> {
  const started = await standingFor(cell);
  const drive = driving(cell, started, "redeploy");

  await drive("env-greeting", ["env", "set", "GREETING", greeting]);
  await drive("deploy", ["deploy", "--yes"]);
  return deployment(cell, started);
}

async function rollback(cell: CellContext): Promise<Deployment> {
  const started = await standingFor(cell);

  await driving(cell, started, "rollback")("rollback", ["rollback"]);
  return deployment(cell, started);
}

async function stillRecorded(slug: string): Promise<boolean> {
  const target = box();
  const said = await ssh(
    target,
    DEPLOY_LOGIN,
    `test -e '${PROJECT_RECORDS}/${recordFile(slug)}' && echo held || echo gone`,
  );
  return said.trim() === "held";
}

async function destroy(cell: CellContext): Promise<void> {
  const started = standing.get(cell.slug);
  if (!started) {
    return;
  }
  standing.delete(cell.slug);
  const args = ["--config", CONFIG, "destroy", "production", "--yes"];
  try {
    await runOcel(cell, started.dir, "destroy", "destroy", args, started.env);
  } catch (refused) {
    if (await stillRecorded(cell.slug)) {
      throw refused;
    }
  } finally {
    await started.gateway.close();
  }
}

async function list(): Promise<string[]> {
  const target = box();
  const listing = await ssh(
    target,
    DEPLOY_LOGIN,
    `test -d '${PROJECT_RECORDS}' && { ls -1d '${PROJECT_RECORDS}'/${HARNESS_PREFIX}*.rec 2>/dev/null || true; } || echo ${NO_RECORDS_TIER}`,
  );
  return slugsOf(listing);
}

async function sweep(runId: string): Promise<void> {
  const environment = await guard();
  if (environment !== "vps.incus") {
    throw new Error(
      "sweep destroys every project a harness run left on the box, and this box is not the disposable incus one: " +
        "reclaim a real box by naming what to destroy yourself",
    );
  }
  const stranded = strandedSlugs(await list(), runId);
  for (const slug of stranded) {
    const dir = await boxConfig(path.join(outputRoot, "vps", "sweep", slug), slug, DEPLOY_LOGIN);
    await ocel(dir, ["destroy", "production", "--yes"], boxEnv(DEPLOY_LOGIN));
  }
}

export const vpsTarget: Target = {
  name: "vps",
  concurrency: 2,
  legTimeoutMs: 600_000,
  legs: ["up", "contract", "redeploy", "rollback", "destroy"],
  guard,
  setup,
  up,
  redeploy,
  rollback,
  destroy,
  list,
  stands: stillRecorded,
  sweep,
};
