import { type ChildProcess, spawn } from "node:child_process";
import { access } from "node:fs/promises";
import { createServer } from "node:net";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { redact } from "../contract";
import { live, relay } from "../live";
import { ocelBin } from "../paths";
import { appHomes, stateComplaint } from "../workspace";
import type { CellContext } from "./types";

const HEALTH_TIMEOUT_MS = 120_000;

export type Running = { app: string; port: number; child: ChildProcess; output: () => string };

export type Standing = { dir: string; apps: Running[] };

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

async function waitForHealth(url: string, cell: Running): Promise<void> {
  const deadline = Date.now() + HEALTH_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if (cell.child.exitCode !== null || cell.child.signalCode !== null) {
      throw new Error(`ocel dev exited before ${url} answered:\n${redact(cell.output())}`);
    }
    try {
      const res = await fetch(url);
      if (res.ok) {
        return;
      }
    } catch {}
    await delay(500);
  }
  throw new Error(`${url} never became healthy:\n${redact(cell.output())}`);
}

export async function serve(
  cell: CellContext,
  dir: string,
  env: NodeJS.ProcessEnv,
  app: string,
  args: string[],
): Promise<Running> {
  const port = await freePort();
  const child = spawn(ocelBin, args, {
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
  const say = live(`${cell.name} up/dev-${app} |`);
  relay(child.stdout, say);
  relay(child.stderr, say);

  const handle: Running = { app, port, child, output: () => captured };
  try {
    await waitForHealth(`http://127.0.0.1:${port}/health`, handle);
  } finally {
    await cell.evidence.write("up", `dev-${app}.log`, captured);
  }
  return handle;
}

export async function stop(handle: Running): Promise<void> {
  const { child } = handle;
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

export async function stateStaysHome(cell: CellContext, dir: string): Promise<void> {
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

async function answering(port: number): Promise<boolean> {
  try {
    const res = await fetch(`http://127.0.0.1:${port}/health`);
    return res.ok;
  } catch {
    return false;
  }
}

export async function stillServing(running: Map<string, Standing>): Promise<string[]> {
  const alive: string[] = [];
  for (const [slug, standing] of running) {
    for (const handle of standing.apps) {
      if (await answering(handle.port)) {
        alive.push(slug);
        break;
      }
    }
  }
  return alive;
}

export async function stopStanding(cell: CellContext, standing: Standing): Promise<void> {
  for (const handle of standing.apps) {
    await stop(handle);
    await cell.evidence.write("destroy", `dev-${handle.app}.log`, handle.output());
  }
}

export function baseUrls(cell: CellContext, urls: Map<string, string>, target: string) {
  return (app: string): string => {
    const url = urls.get(app);
    if (!url) {
      throw new Error(`${cell.name} has no app named ${app} on ${target}`);
    }
    return url;
  };
}
