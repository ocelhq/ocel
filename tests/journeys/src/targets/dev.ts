import { type ChildProcess, spawn } from "node:child_process";
import { access, rm } from "node:fs/promises";
import { createServer } from "node:net";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import {
  applyConsoleEnvDefaults,
  consoleUrl,
  HARNESS_ONLY_ENV,
} from "@ocel-tests/shared/env";
import { INITIAL_GREETING, redact, SECRET_TOKEN } from "../contract";
import type { ExpectationEnvironment } from "../expectations/types";
import { JOURNEY_CONFIG } from "../config";
import { cellEnv, runOcel, treeRoot, workTree } from "../ocel";
import { ocelBin } from "../paths";
import { migrates } from "../rows";
import { appCommand, appHomes, migrateCommand, stateComplaint } from "../workspace";
import type { CellContext, Deployment, Target } from "./types";

const HEALTH_TIMEOUT_MS = 120_000;

const START_CONSOLE = [
  "docker compose up -d postgres ocel-cloud minio",
  "pnpm --filter @console/db db:push",
  "pnpm --filter @console/web dev",
].join(" && ");

type Running = { app: string; port: number; child: ChildProcess; output: () => string };

type Standing = { dir: string; apps: Running[] };

const running = new Map<string, Standing>();

let seeded: Promise<string> | undefined;

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

async function guard(): Promise<ExpectationEnvironment> {
  const url = `${consoleUrl()}/api/projects`;
  const because = (said: string) =>
    new Error(`${url} ${said}; the journey harness never starts it. Run: ${START_CONSOLE}`);
  let res: Response;
  try {
    res = await fetch(url, { method: "GET" });
  } catch (error) {
    throw because(`is not answering (${String(error)})`);
  }
  if (res.status !== 401) {
    throw because(`answered ${res.status}, and the console answers an unauthorized list with 401`);
  }
  return "dev";
}

async function accessToken(): Promise<string> {
  seeded ??= (async () => {
    applyConsoleEnvDefaults();
    const existing = process.env.OCEL_ACCESS_TOKEN;
    if (existing) {
      return existing;
    }
    const { seed } = await import("@ocel-tests/shared/seed");
    return (await seed("Journey")).token;
  })();
  return seeded;
}

function childEnv(token: string, cell: CellContext): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    OCEL_ACCESS_TOKEN: token,
    OCEL_API_URL: consoleUrl(),
    ...cellEnv(cell),
  };
  for (const name of HARNESS_ONLY_ENV) {
    delete env[name];
  }
  return env;
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

async function serve(
  cell: CellContext,
  dir: string,
  env: NodeJS.ProcessEnv,
  app: string,
): Promise<Running> {
  const port = await freePort();
  const child = spawn(ocelBin, ["dev", "--", ...appCommand(cell.fixture, app)], {
    cwd: dir,
    env: { ...env, PORT: String(port), APP_NAME: app },
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let captured = "";
  const capture = (chunk: Buffer) => {
    captured += String(chunk);
  };
  child.stdout?.on("data", capture);
  child.stderr?.on("data", capture);

  const handle: Running = { app, port, child, output: () => captured };
  try {
    await waitForHealth(`http://127.0.0.1:${port}/health`, handle);
  } finally {
    await cell.evidence.write("up", `dev-${app}.log`, captured);
  }
  return handle;
}

async function stateStaysHome(cell: CellContext, dir: string): Promise<void> {
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

async function up(cell: CellContext): Promise<Deployment> {
  const token = await accessToken();
  const dir = await workTree(cell, "dev");
  const env = { ...childEnv(token, cell), OCEL_CONFIG: path.join(dir, JOURNEY_CONFIG) };

  await runOcel(cell, dir, "up", "console-link", ["console", "link", "--create", cell.slug], env);
  await runOcel(cell, dir, "up", "env-greeting", ["env", "set", "GREETING", INITIAL_GREETING], env);
  await runOcel(cell, dir, "up", "env-secret", ["env", "set", "SECRET_TOKEN", SECRET_TOKEN], env);
  if (migrates(cell.fixture.rows)) {
    await runOcel(cell, dir, "up", "migrate", ["run", "--", ...migrateCommand()], env);
  }

  const standing: Standing = { dir, apps: [] };
  running.set(cell.slug, standing);
  const urls = new Map<string, string>();
  for (const app of cell.fixture.apps) {
    const handle = await serve(cell, dir, env, app);
    standing.apps.push(handle);
    urls.set(app, `http://127.0.0.1:${handle.port}`);
  }
  await stateStaysHome(cell, dir);

  await cell.evidence.write(
    "up",
    "deployment.json",
    `${JSON.stringify({ slug: cell.slug, dir, apps: Object.fromEntries(urls) }, null, 2)}\n`,
  );

  return {
    baseUrl: (app) => {
      const url = urls.get(app);
      if (!url) {
        throw new Error(`${cell.name} has no app named ${app} on dev`);
      }
      return url;
    },
    fetch: (...args) => fetch(...args),
  };
}

async function stop(handle: Running): Promise<void> {
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

async function destroy(cell: CellContext): Promise<void> {
  const standing = running.get(cell.slug);
  if (!standing) {
    return;
  }
  for (const handle of standing.apps) {
    await stop(handle);
    await cell.evidence.write("destroy", `dev-${handle.app}.log`, handle.output());
  }
  await rm(treeRoot(cell, "dev"), { recursive: true, force: true });
}

async function answering(port: number): Promise<boolean> {
  try {
    const res = await fetch(`http://127.0.0.1:${port}/health`);
    return res.ok;
  } catch {
    return false;
  }
}

async function consoleProjects(): Promise<string[]> {
  const token = await accessToken();
  const res = await fetch(`${consoleUrl()}/api/projects`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) {
    throw new Error(`the console answered ${res.status} listing this account's projects`);
  }
  const projects = (await res.json()) as Array<{ slug: string }>;
  return projects.map((project) => project.slug);
}

async function list(): Promise<string[]> {
  const live = new Set(await consoleProjects());
  for (const [slug, standing] of running) {
    for (const handle of standing.apps) {
      if (await answering(handle.port)) {
        live.add(slug);
      }
    }
  }
  return [...live];
}

export const devTarget: Target = {
  name: "dev",
  concurrency: 4,
  legTimeoutMs: 180_000,
  legs: ["up", "contract", "destroy"],
  guard,
  setup: async () => {
    await guard();
    await accessToken();
  },
  up,
  destroy,
  list,
  stands: async (slug) => (await list()).includes(slug),
  sweep: async () => {},
};
