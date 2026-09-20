import { type ChildProcess, execFile, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { access, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { promisify } from "node:util";
import { HARNESS_ONLY_ENV } from "@ocel-tests/shared/env";
import { migrates, setsEnv, setsSecret } from "../checks";
import { INITIAL_GREETING, redact, SECRET_TOKEN, UNCAPPED_BODY_BYTES } from "../checks/context";
import { journeyConfigIn } from "../config";
import type { Lane } from "../matrix/types";
import { configTree, runOcel, treeRoot, workTree } from "../ocel";
import { ocelBin } from "../paths";
import type { PrepareFailures } from "../prepare";
import { progress, relay } from "../progress";
import type { CellUnderTest } from "../run/cellRun";
import { appCommand, appHomes, migrateCommand, stateComplaint } from "../workspace";
import type { Deployment, Sweeper, Target } from "./types";

const HEALTH_TIMEOUT_MS = 120_000;

const STACK_STOPS_WITHIN_MS = 45_000;

const DOTFILE = ".env";

const PROJECT_LABEL = "dev.ocel.project";

const START_DOCKER =
  "ocel dev runs a declared postgres and bucket in containers, and the journey harness never starts a daemon. Start docker, or point DOCKER_HOST at one that is running";

const run = promisify(execFile);

type ServedApp = { app: string; port: number; child: ChildProcess; output: () => string };

type ServedApps = { dir: string; apps: ServedApp[] };

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

async function waitForHealth(url: string, served: ServedApp): Promise<void> {
  const deadline = Date.now() + HEALTH_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if (exited(served.child)) {
      throw new Error(`ocel dev exited before ${url} answered:\n${redact(served.output())}`);
    }
    try {
      const res = await fetch(url);
      if (res.ok) {
        return;
      }
    } catch {}
    await delay(500);
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

async function answering(port: number): Promise<boolean> {
  try {
    const res = await fetch(`http://127.0.0.1:${port}/health`);
    return res.ok;
  } catch {
    return false;
  }
}

export function startedResources(said: string): boolean {
  return /^\S+ "[^"]+" → /m.test(said);
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

async function removeStack(project: string): Promise<void> {
  const containers = await labelled("container", project);
  if (containers.length > 0) {
    await run("docker", ["rm", "--force", "--volumes", ...containers]);
  }
  const volumes = await labelled("volume", project);
  if (volumes.length > 0) {
    await run("docker", ["volume", "rm", "--force", ...volumes]);
  }
}

async function writeDotfile(cell: CellUnderTest, dir: string): Promise<void> {
  const lines: string[] = [];
  if (setsEnv(cell.fixture.checks)) {
    lines.push(`GREETING=${INITIAL_GREETING}`);
  }
  if (setsSecret(cell.fixture.checks)) {
    lines.push(`SECRET_TOKEN=${SECRET_TOKEN}`);
  }
  await writeFile(path.join(dir, DOTFILE), `${lines.join("\n")}\n`, "utf8");
  await cell.evidence.write("deploy", DOTFILE, `${lines.join("\n")}\n`);
}

export class DevTarget implements Target {
  readonly name = "dev";
  readonly workers = 4;
  readonly maxRequestBodyBytes = UNCAPPED_BODY_BYTES;
  readonly stepTimeoutMs = 180_000;

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
    if (migrates(cell.fixture.checks)) {
      await runOcel(cell, dir, "deploy", "migrate", ["run", "--", ...migrateCommand()], env);
    }

    const served: ServedApps = { dir, apps: [] };
    this.served.set(cell.slug, served);
    const urls = new Map<string, string>();
    for (const app of cell.fixture.apps) {
      const one = await this.serve(cell, dir, env, app);
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
      "deploy",
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
    await removeStack(devProject(configTree(cell, this.name)));
    await rm(treeRoot(cell, this.name), { recursive: true, force: true });
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
        if (await answering(one.port)) {
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
    const log = progress(`${cell.name} deploy/dev-${app} |`);
    relay(child.stdout, log);
    relay(child.stderr, log);

    const served: ServedApp = { app, port, child, output: () => captured };
    try {
      await waitForHealth(`http://127.0.0.1:${port}/health`, served);
    } finally {
      await cell.evidence.write("deploy", `dev-${app}.log`, captured);
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
    const holding: string[] = [];
    for (const candidate of [dir, ...appHomes(cell.fixture).map((home) => path.join(dir, home))]) {
      try {
        await access(path.join(candidate, ".ocel"));
        holding.push(candidate);
      } catch {}
    }
    const complaint = stateComplaint(dir, holding);
    if (complaint) {
      throw new Error(complaint);
    }
  }
}
