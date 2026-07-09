import { type ChildProcess, spawn } from "node:child_process";
import { readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const here = path.dirname(fileURLToPath(import.meta.url));
// tests/e2e/src -> repo root.
export const repoRoot = path.resolve(here, "..", "..", "..");
export const examplesDir = path.join(repoRoot, "examples");

// The built Go CLI. `cd cli && go build -o dist/ocel ./ocel` produces this
// (output goes to cli/dist/ - a gitignored path - because the CLI's own main
// package already lives at cli/ocel/, so `-o ocel` would collide with that
// directory). Override with OCEL_BIN to point at any binary.
export const ocelBin =
  process.env.OCEL_BIN ?? path.join(repoRoot, "cli", "dist", "ocel");

export const apiUrl = process.env.OCEL_API_URL ?? "http://localhost:3000";

// The committed placeholder each example's ocel.config.ts is reset to after a
// run, matching what's checked in.
const placeholderConfig = `import { defineConfig } from "ocel/config";


// Placeholder committed so the example type-checks and reads as complete.
// \`ocel init\` overwrites this with the real projectId; the e2e harness deletes
// it before running init and restores it afterwards.
export default defineConfig({ projectId: "placeholder" });
`;

export type ExampleSpec = {
  framework: "next" | "express" | "hono";
  dir: string;
  port: number;
  /** URL path of the readiness probe. */
  healthPath: string;
  /** Base path of the todos CRUD routes (Next mounts them under /api). */
  todosPath: string;
  /** Command (argv) `ocel run --` executes to migrate. */
  migrateCmd: string[];
  /** Command (argv) `ocel dev --` executes to start the server. */
  startCmd: string[];
};

// Distinct ports so the three specs can run their dev servers in parallel.
export const examples: Record<ExampleSpec["framework"], ExampleSpec> = {
  next: {
    framework: "next",
    dir: path.join(examplesDir, "next"),
    port: 3101,
    healthPath: "/api/health",
    todosPath: "/api/todos",
    migrateCmd: ["pnpm", "migrate"],
    startCmd: ["pnpm", "start"],
  },
  express: {
    framework: "express",
    dir: path.join(examplesDir, "express"),
    port: 3102,
    healthPath: "/health",
    todosPath: "/todos",
    migrateCmd: ["pnpm", "migrate"],
    startCmd: ["pnpm", "start"],
  },
  hono: {
    framework: "hono",
    dir: path.join(examplesDir, "hono"),
    port: 3103,
    healthPath: "/health",
    todosPath: "/todos",
    migrateCmd: ["pnpm", "migrate"],
    startCmd: ["pnpm", "start"],
  },
};

// The environment the CLI (and, through it, the example app) inherits.
// OCEL_ACCESS_TOKEN authenticates via the credentials env fallback; PORT tells
// the example which port to bind.
function ocelEnv(token: string, port: number): NodeJS.ProcessEnv {
  return {
    ...process.env,
    OCEL_ACCESS_TOKEN: token,
    OCEL_API_URL: apiUrl,
    PORT: String(port),
  };
}

type RunResult = { code: number | null; stdout: string; stderr: string };

// Runs the CLI to completion (used for `init` and `run`), capturing output.
function runOcel(
  args: string[],
  spec: ExampleSpec,
  token: string,
): Promise<RunResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(ocelBin, args, {
      cwd: spec.dir,
      env: ocelEnv(token, spec.port),
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => {
      stdout += d.toString();
    });
    child.stderr.on("data", (d) => {
      stderr += d.toString();
    });
    child.on("error", reject);
    child.on("close", (code) => resolve({ code, stdout, stderr }));
  });
}

export async function deletePlaceholderConfig(spec: ExampleSpec) {
  await rm(path.join(spec.dir, "ocel.config.ts"), { force: true });
}

export async function resetPlaceholderConfig(spec: ExampleSpec) {
  await writeFile(path.join(spec.dir, "ocel.config.ts"), placeholderConfig);
}

export async function runInit(
  spec: ExampleSpec,
  token: string,
  runId: string,
): Promise<RunResult> {
  const name = `e2e-${spec.framework}-${runId}`;
  const result = await runOcel(["init", name, "--yes"], spec, token);
  if (result.code !== 0) {
    throw new Error(
      `ocel init failed (code ${result.code})\nstdout: ${result.stdout}\nstderr: ${result.stderr}`,
    );
  }
  return result;
}

export async function runMigrate(
  spec: ExampleSpec,
  token: string,
): Promise<RunResult> {
  const result = await runOcel(
    ["run", "--", ...spec.migrateCmd],
    spec,
    token,
  );
  if (result.code !== 0) {
    throw new Error(
      `ocel run (migrate) failed (code ${result.code})\nstdout: ${result.stdout}\nstderr: ${result.stderr}`,
    );
  }
  return result;
}

export type DevHandle = {
  child: ChildProcess;
  // Everything the CLI/app has written to stdout+stderr so far, so a crash
  // can be reported with its own diagnostics.
  output: () => string;
  stop: () => Promise<void>;
};

// Starts `ocel dev -- <startCmd>` in its own process group (detached) so the
// whole tree - ocel, the app, anything it forks - can be torn down together.
export function startDev(spec: ExampleSpec, token: string): DevHandle {
  const child = spawn(ocelBin, ["dev", "--", ...spec.startCmd], {
    cwd: spec.dir,
    env: ocelEnv(token, spec.port),
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  // Surface app/CLI output under the test for debugging, and retain it so an
  // early crash can be reported with the process's own output.
  let captured = "";
  const onData = (d: Buffer) => {
    captured += d.toString();
    process.stderr.write(`[${spec.framework} dev] ${d}`);
  };
  child.stdout?.on("data", onData);
  child.stderr?.on("data", onData);

  const stop = async () => {
    if (child.pid && child.exitCode === null) {
      try {
        // Negative pid targets the whole process group.
        process.kill(-child.pid, "SIGTERM");
      } catch {
        // Already gone.
      }
      await delay(500);
      try {
        process.kill(-child.pid, "SIGKILL");
      } catch {
        // Already gone.
      }
    }
  };

  return { child, output: () => captured, stop };
}

// Polls the readiness probe until it returns 200 or the timeout elapses. If
// the dev process exits before then (auth failure, port conflict, bad
// config), it fails fast with the process's exit code and output rather than
// waiting out the full timeout.
export async function waitForHealth(
  spec: ExampleSpec,
  dev: DevHandle,
  timeoutMs = 90_000,
): Promise<void> {
  const url = `http://localhost:${spec.port}${spec.healthPath}`;
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (dev.child.exitCode !== null || dev.child.signalCode !== null) {
      const how =
        dev.child.exitCode !== null
          ? `code ${dev.child.exitCode}`
          : `signal ${dev.child.signalCode}`;
      throw new Error(
        `ocel dev for ${spec.framework} exited early (${how}) before ${url} became ready:\n${dev.output()}`,
      );
    }
    try {
      const res = await fetch(url);
      if (res.ok) {
        return;
      }
    } catch {
      // Server not up yet.
    }
    await delay(500);
  }
  throw new Error(`health check never became ready at ${url}`);
}

export const base = (spec: ExampleSpec) => `http://localhost:${spec.port}`;
