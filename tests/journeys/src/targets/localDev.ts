import { type ChildProcess, spawn } from "node:child_process";
import { access, rm } from "node:fs/promises";
import { createServer } from "node:net";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { redact, UNCAPPED_BODY_BYTES } from "../checks/context";
import { live, relay } from "../live";
import type { Lane, TargetName } from "../matrix/types";
import { treeRoot, workTree } from "../ocel";
import { ocelBin } from "../paths";
import type { PrepareFailures } from "../prepare";
import { appHomes, stateComplaint } from "../workspace";
import type { CellContext, Deployment, Sweeper, Target } from "./types";

const HEALTH_TIMEOUT_MS = 120_000;

export type ServedApp = { app: string; port: number; child: ChildProcess; output: () => string };

export type ServedApps = { dir: string; apps: ServedApp[] };

export async function freePort(): Promise<number> {
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
    if (served.child.exitCode !== null || served.child.signalCode !== null) {
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

async function stop(served: ServedApp): Promise<void> {
  const { child } = served;
  if (!child.pid || child.exitCode !== null) {
    return;
  }
  try {
    process.kill(-child.pid, "SIGTERM");
  } catch {}
  await delay(500);
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

export abstract class LocalDevTarget implements Target {
  abstract readonly name: TargetName;
  readonly workers = 4;
  readonly maxRequestBodyBytes = UNCAPPED_BODY_BYTES;
  readonly stepTimeoutMs = 180_000;
  abstract readonly sweeper: Sweeper;

  private readonly served = new Map<string, ServedApps>();

  abstract detectLane(): Promise<Lane>;

  abstract prepareProcess(): Promise<void>;

  protected abstract ocelEnv(dir: string): Promise<NodeJS.ProcessEnv>;

  protected abstract beforeServing(
    cell: CellContext,
    dir: string,
    env: NodeJS.ProcessEnv,
  ): Promise<void>;

  protected abstract serveArgs(cell: CellContext, app: string): string[];

  protected abstract afterStopping(cell: CellContext): Promise<void>;

  async prepareLane(): Promise<PrepareFailures> {
    return {};
  }

  async deploy(cell: CellContext): Promise<Deployment> {
    const dir = await workTree(cell, this.name);
    const env = await this.ocelEnv(dir);
    await this.beforeServing(cell, dir, env);

    const served: ServedApps = { dir, apps: [] };
    this.served.set(cell.slug, served);
    const urls = new Map<string, string>();
    for (const app of cell.fixture.apps) {
      const one = await this.serve(cell, dir, env, app);
      served.apps.push(one);
      urls.set(app, `http://127.0.0.1:${one.port}`);
    }
    await this.stateStaysHome(cell, dir);

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

  async destroy(cell: CellContext): Promise<void> {
    const served = this.served.get(cell.slug);
    if (served) {
      for (const one of served.apps) {
        await stop(one);
        await cell.evidence.write("destroy", `dev-${one.app}.log`, one.output());
      }
      await rm(treeRoot(cell, this.name), { recursive: true, force: true });
      this.served.delete(cell.slug);
    }
    await this.afterStopping(cell);
  }

  protected async stillServing(): Promise<string[]> {
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
    cell: CellContext,
    dir: string,
    env: NodeJS.ProcessEnv,
    app: string,
  ): Promise<ServedApp> {
    const port = await freePort();
    const child = spawn(ocelBin, this.serveArgs(cell, app), {
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
    const say = live(`${cell.name} deploy/dev-${app} |`);
    relay(child.stdout, say);
    relay(child.stderr, say);

    const served: ServedApp = { app, port, child, output: () => captured };
    try {
      await waitForHealth(`http://127.0.0.1:${port}/health`, served);
    } finally {
      await cell.evidence.write("deploy", `dev-${app}.log`, captured);
    }
    return served;
  }

  private async stateStaysHome(cell: CellContext, dir: string): Promise<void> {
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
