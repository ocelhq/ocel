import { type ChildProcess, execFile, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { access, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { promisify, stripVTControlCharacters } from "node:util";
import { HARNESS_ONLY_ENV } from "@ocel-tests/shared/env";
import { migrates, setsEnv, setsSecret } from "../checks";
import {
  INITIAL_GREETING,
  JOURNEY_NONCE_ENV,
  redact,
  SECRET_TOKEN,
  setsJourneyNonce,
  UNCAPPED_BODY_BYTES,
} from "../checks/context";
import { journeyConfigIn } from "../config";
import type { Lane, Phase } from "../matrix/types";
import { configTree, recordOutput, runOcel, treeRoot, workTree } from "../ocel";
import { ocelBin } from "../paths";
import type { PrepareFailures } from "../prepare";
import { progress, relay } from "../progress";
import type { CellUnderTest } from "../run/cellRun";
import { appCommand, appHomes, migrateCommand, stateComplaint } from "../workspace";
import type { Deployment, Exposure, Restart, Sweeper, Target } from "./types";

export const RESOLVE_TIMEOUT_MS = 240_000;

export const HEALTH_TIMEOUT_MS = 120_000;

const HEALTH_PROBE_TIMEOUT_MS = 5_000;

const STACK_STOPS_WITHIN_MS = 45_000;

const DOTFILE = ".env";

const PROJECT_LABEL = "dev.ocel.project";

const START_DOCKER =
  "ocel dev runs each declared postgres, bucket and kv store, and the database topics and tasks run on, in a container, and the journey harness never starts a daemon. Start docker, or point DOCKER_HOST at one that is running";

const run = promisify(execFile);

type ServedApp = { app: string; port: number; child: ChildProcess; output: () => string };

type ServedApps = { dir: string; env: NodeJS.ProcessEnv; apps: ServedApp[]; said: string[] };

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.unref();
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (typeof address !== "object" || address === null) {
        reject(new Error("could not read the port the kernel handed out"));
        return;
      }
      const { port } = address;
      server.close(() => resolve(port));
    });
  });
}

export function resolvedEnvironment(said: string): boolean {
  return /✓ (Resolved the app's environment|Connected to the running `ocel dev`)/.test(
    stripVTControlCharacters(said),
  );
}

export function healthDeadline(started: number, resolvedAt: number | undefined): number {
  return resolvedAt === undefined
    ? started + RESOLVE_TIMEOUT_MS + HEALTH_TIMEOUT_MS
    : resolvedAt + HEALTH_TIMEOUT_MS;
}

async function waitForHealth(url: string, served: ServedApp): Promise<void> {
  const started = Date.now();
  let resolvedAt: number | undefined;
  while (Date.now() < healthDeadline(started, resolvedAt)) {
    if (resolvedAt === undefined && resolvedEnvironment(served.output())) {
      resolvedAt = Date.now();
    }
    if (resolvedAt === undefined && Date.now() >= started + RESOLVE_TIMEOUT_MS) {
      break;
    }
    if (exited(served.child)) {
      throw new Error(`ocel dev exited before ${url} answered:\n${redact(served.output())}`);
    }
    if (await answersHealth(url)) {
      return;
    }
    await delay(500);
  }
  if (resolvedAt === undefined) {
    throw new Error(
      `ocel dev did not resolve the app's environment within ${RESOLVE_TIMEOUT_MS / 1000}s:\n${redact(served.output())}`,
    );
  }
  throw new Error(`${url} never became healthy:\n${redact(served.output())}`);
}

function exited(child: ChildProcess): boolean {
  return child.exitCode !== null || child.signalCode !== null;
}

async function stop(served: ServedApp): Promise<void> {
  const { child } = served;
  if (!child.pid || exited(child)) {
    return;
  }
  try {
    process.kill(-child.pid, "SIGTERM");
  } catch {}
  const deadline = Date.now() + STACK_STOPS_WITHIN_MS;
  while (!exited(child) && Date.now() < deadline) {
    await delay(250);
  }
  try {
    process.kill(-child.pid, "SIGKILL");
  } catch {}
}

export async function answersHealth(
  url: string,
  within = HEALTH_PROBE_TIMEOUT_MS,
): Promise<boolean> {
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(within) });
    return res.ok;
  } catch {
    return false;
  }
}

export function startedResources(said: string): boolean {
  return /^INFO\s+\S+ "[^"]+" → /m.test(said);
}

export function devProject(dir: string): string {
  const readable = path
    .basename(dir)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  const digest = createHash("sha256").update(path.normalize(dir)).digest("hex").slice(0, 8);
  return `${readable}-${digest}`;
}

async function labelled(kind: "container" | "volume", project: string): Promise<string[]> {
  const filter = ["--filter", `label=${PROJECT_LABEL}=${project}`];
  const args =
    kind === "container"
      ? ["ps", "--all", "--quiet", ...filter]
      : ["volume", "ls", "--quiet", ...filter];
  const { stdout } = await run("docker", args);
  return stdout.split("\n").filter((line) => line.trim() !== "");
}

export function findKeptRunning(before: Map<string, string>, after: Map<string, string>): string[] {
  return [...after].filter(([name, started]) => before.get(name) === started).map(([name]) => name);
}

async function readStartTimes(project: string): Promise<Map<string, string>> {
  const containers = await labelled("container", project);
  if (containers.length === 0) {
    return new Map();
  }
  const { stdout } = await run("docker", [
    "inspect",
    "--format",
    "{{.Name}} {{.State.StartedAt}}",
    ...containers,
  ]);
  return new Map(
    stdout
      .split("\n")
      .filter((line) => line.trim() !== "")
      .map((line) => line.trim().split(" ") as [string, string]),
  );
}

export function volumesIn(inspected: string): string[] {
  return [...new Set(inspected.split(/\s+/).filter((name) => name !== ""))];
}

async function mountedVolumes(containers: string[]): Promise<string[]> {
  if (containers.length === 0) {
    return [];
  }
  const { stdout } = await run("docker", [
    "inspect",
    "--format",
    '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}',
    ...containers,
  ]);
  return volumesIn(stdout);
}

async function existingVolumes(names: string[]): Promise<string[]> {
  const { stdout } = await run("docker", ["volume", "ls", "--quiet"]);
  const listed = new Set(volumesIn(stdout));
  return names.filter((name) => listed.has(name));
}

async function removeStack(project: string): Promise<string[]> {
  const containers = await labelled("container", project);
  const mounted = await mountedVolumes(containers);
  if (containers.length > 0) {
    await run("docker", ["rm", "--force", "--volumes", ...containers]);
  }
  const volumes = await labelled("volume", project);
  if (volumes.length > 0) {
    await run("docker", ["volume", "rm", "--force", ...volumes]);
  }
  const unlabelled = await existingVolumes(mounted);
  if (unlabelled.length > 0) {
    await run("docker", ["volume", "rm", "--force", ...unlabelled]);
  }
  return unlabelled;
}

async function writeDotfile(cell: CellUnderTest, dir: string): Promise<void> {
  const lines: string[] = [];
  if (setsEnv(cell.fixture.checks)) {
    lines.push(`GREETING=${INITIAL_GREETING}`);
  }
  if (setsSecret(cell.fixture.checks)) {
    lines.push(`SECRET_TOKEN=${SECRET_TOKEN}`);
  }
  if (setsJourneyNonce(cell.fixture.checks)) {
    lines.push(`${JOURNEY_NONCE_ENV}=${cell.journeyNonce}`);
  }
  const written = `${lines.join("\n")}\n`;
  await writeFile(path.join(dir, DOTFILE), written, "utf8");
  await cell.evidence.write("deploy", DOTFILE, redact(written, [cell.journeyNonce]));
}

export class DevTarget implements Target, Restart, Exposure {
  readonly name = "dev";
  readonly workers = 4;
  readonly maxRequestBodyBytes = UNCAPPED_BODY_BYTES;
  readonly stepTimeoutMs = 420_000;

  private readonly served = new Map<string, ServedApps>();

  readonly sweeper: Sweeper = {
    list: () => this.stillServing(),
    exists: async (slug) => (await this.stillServing()).includes(slug),
    sweepStale: async () => {},
    sweepRun: async () => {},
  };

  async detectLane(): Promise<Lane> {
    try {
      await run("docker", ["version", "--format", "{{.Server.Version}}"]);
    } catch (error) {
      throw new Error(`no docker daemon answers (${String(error)}). ${START_DOCKER}.`);
    }
    return "dev";
  }

  async prepareProcess(): Promise<void> {
    await this.detectLane();
  }

  async prepareLane(): Promise<PrepareFailures> {
    return {};
  }

  async deploy(cell: CellUnderTest): Promise<Deployment> {
    const dir = await workTree(cell, this.name);
    const env = this.ocelEnv(dir);
    await writeDotfile(cell, dir);
    const served: ServedApps = { dir, env, apps: [], said: [] };
    this.served.set(cell.slug, served);
    if (migrates(cell.fixture.checks)) {
      await recordOutput(
        served.said,
        runOcel(cell, dir, "deploy", "migrate", ["run", "--", ...migrateCommand()], env),
      );
    }
    return this.serveApps(cell, served, "deploy");
  }

  async restart(cell: CellUnderTest): Promise<Deployment> {
    const project = devProject(this.servedFor(cell).dir);
    const before = await readStartTimes(project);
    const deployed = await this.serveApps(cell, await this.stopApps(cell), "restart");
    const kept = findKeptRunning(before, await readStartTimes(project));
    if (kept.length > 0) {
      throw new Error(
        `${kept.join(", ")} kept running while ocel dev stopped and started again, so nothing ${cell.name} declared was restarted`,
      );
    }
    return deployed;
  }

  async readExposed(cell: CellUnderTest): Promise<string> {
    const served = this.servedFor(cell);
    const containers = await labelled("container", devProject(served.dir));
    const inspected =
      containers.length === 0 ? "" : (await run("docker", ["inspect", ...containers])).stdout;
    const processes = (await run("ps", ["-eo", "args"])).stdout;
    return [...served.said, ...served.apps.map((one) => one.output()), inspected, processes].join(
      "\n",
    );
  }

  private servedFor(cell: CellUnderTest): ServedApps {
    const served = this.served.get(cell.slug);
    if (!served) {
      throw new Error(`${cell.name} is not served on ${this.name}`);
    }
    return served;
  }

  private async stopApps(cell: CellUnderTest): Promise<ServedApps> {
    const served = this.servedFor(cell);
    for (const one of served.apps.splice(0)) {
      await stop(one);
      served.said.push(one.output());
    }
    return served;
  }

  private async serveApps(
    cell: CellUnderTest,
    served: ServedApps,
    phase: Phase,
  ): Promise<Deployment> {
    const { dir, env, said } = served;
    const urls = new Map<string, string>();
    for (const app of cell.fixture.apps) {
      const one = await this.serve(cell, dir, env, app, phase, said);
      served.apps.push(one);
      urls.set(app, `http://127.0.0.1:${one.port}`);
    }
    await this.stateStaysHome(cell, dir);
    if (
      migrates(cell.fixture.checks) ||
      served.apps.some((one) => startedResources(one.output()))
    ) {
      await this.stackIsTraceable(dir);
    }

    await cell.evidence.write(
      phase,
      "deployment.json",
      `${JSON.stringify({ slug: cell.slug, dir, apps: Object.fromEntries(urls) }, null, 2)}\n`,
    );

    return {
      baseUrl: (app) => {
        const url = urls.get(app);
        if (!url) {
          throw new Error(`${cell.name} has no app named ${app} on ${this.name}`);
        }
        return url;
      },
      fetch: (...args) => fetch(...args),
    };
  }

  async destroy(cell: CellUnderTest): Promise<void> {
    for (const one of this.served.get(cell.slug)?.apps ?? []) {
      await stop(one);
      await cell.evidence.write("destroy", `dev-${one.app}.log`, one.output());
    }
    this.served.delete(cell.slug);
    const project = devProject(configTree(cell, this.name));
    const unlabelled = await removeStack(project);
    await rm(treeRoot(cell, this.name), { recursive: true, force: true });
    if (unlabelled.length > 0) {
      throw new Error(
        `ocel dev mounted ${unlabelled.join(", ")} into the containers labelled ${PROJECT_LABEL}=${project} without labelling the volume itself, so removing the project's labelled resources leaves its data behind.`,
      );
    }
  }

  private ocelEnv(dir: string): NodeJS.ProcessEnv {
    const env: NodeJS.ProcessEnv = { ...process.env };
    for (const name of [...HARNESS_ONLY_ENV, "OCEL_ACCESS_TOKEN", "OCEL_CONSOLE_URL"]) {
      delete env[name];
    }
    return { ...env, OCEL_CONFIG: path.join(dir, journeyConfigIn(dir)) };
  }

  private async stillServing(): Promise<string[]> {
    const alive: string[] = [];
    for (const [slug, served] of this.served) {
      for (const one of served.apps) {
        if (await answersHealth(`http://127.0.0.1:${one.port}/health`)) {
          alive.push(slug);
          break;
        }
      }
    }
    return alive;
  }

  private async serve(
    cell: CellUnderTest,
    dir: string,
    env: NodeJS.ProcessEnv,
    app: string,
    phase: Phase,
    said: string[],
  ): Promise<ServedApp> {
    const port = await freePort();
    const child = spawn(ocelBin, ["dev", "--", ...appCommand(cell.fixture, app)], {
      cwd: dir,
      env: { ...env, PORT: String(port) },
      detached: true,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let captured = "";
    const capture = (chunk: Buffer) => {
      captured += String(chunk);
    };
    child.stdout?.on("data", capture);
    child.stderr?.on("data", capture);
    const log = progress(`${cell.name} ${phase}/dev-${app} |`);
    relay(child.stdout, log);
    relay(child.stderr, log);

    const served: ServedApp = { app, port, child, output: () => captured };
    try {
      await waitForHealth(`http://127.0.0.1:${port}/health`, served);
    } catch (error) {
      said.push(captured);
      throw error;
    } finally {
      await cell.evidence.write(phase, `dev-${app}.log`, captured);
    }
    return served;
  }

  private async stackIsTraceable(dir: string): Promise<void> {
    const project = devProject(dir);
    if ((await labelled("container", project)).length === 0) {
      throw new Error(
        `no container is labelled ${PROJECT_LABEL}=${project}, so the resources ocel dev started for ${dir} cannot be traced back to it, and nothing here can remove them.`,
      );
    }
  }

  private async stateStaysHome(cell: CellUnderTest, dir: string): Promise<void> {
    const withState: string[] = [];
    for (const candidate of [dir, ...appHomes(cell.fixture).map((home) => path.join(dir, home))]) {
      try {
        await access(path.join(candidate, ".ocel"));
        withState.push(candidate);
      } catch {}
    }
    const complaint = stateComplaint(dir, withState);
    if (complaint) {
      throw new Error(complaint);
    }
  }
}
