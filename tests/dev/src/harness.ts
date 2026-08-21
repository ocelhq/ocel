import { type ChildProcess, spawn } from "node:child_process";
import { rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { nextDotenv } from "./env";

const here = path.dirname(fileURLToPath(import.meta.url));
export const repoRoot = path.resolve(here, "..", "..", "..");
export const examplesDir = path.join(repoRoot, "examples");

export const ocelBin =
  process.env.OCEL_BIN ?? path.join(repoRoot, "cli", "bin", "ocel");

export const apiUrl = process.env.OCEL_API_URL ?? "http://localhost:3000";

export type ExampleSpec = {
  framework: "next" | "express" | "hono" | "fastify";
  dir: string;
  startCmd: string[];
  capabilities: Array<"environment" | "thumbnail">;
};

const ports: Record<ExampleSpec["framework"], number> = {
  next: 3101,
  express: 3102,
  hono: 3103,
  fastify: 3104,
};

export const portFor = (spec: ExampleSpec) => ports[spec.framework];

export const blobEndpoint =
  process.env.OCEL_BLOB_ENDPOINT ?? "http://localhost:9000";

export async function minioReachable(): Promise<boolean> {
  try {
    const res = await fetch(`${blobEndpoint}/minio/health/live`);
    return res.ok;
  } catch {
    return false;
  }
}

export const examples: Record<ExampleSpec["framework"], ExampleSpec> = {
  next: {
    framework: "next",
    dir: path.join(examplesDir, "next"),
    startCmd: ["pnpm", "start"],
    capabilities: ["environment"],
  },
  express: {
    framework: "express",
    dir: path.join(examplesDir, "express"),
    startCmd: ["pnpm", "start"],
    capabilities: ["thumbnail"],
  },
  hono: {
    framework: "hono",
    dir: path.join(examplesDir, "hono"),
    startCmd: ["pnpm", "start"],
    capabilities: [],
  },
  fastify: {
    framework: "fastify",
    dir: path.join(examplesDir, "fastify"),
    startCmd: ["pnpm", "start"],
    capabilities: [],
  },
};

function ocelEnv(token: string, port: number): NodeJS.ProcessEnv {
  return {
    ...process.env,
    OCEL_ACCESS_TOKEN: token,
    OCEL_API_URL: apiUrl,
    PORT: String(port),
  };
}

type RunResult = { code: number | null; stdout: string; stderr: string };

function runOcel(
  args: string[],
  spec: ExampleSpec,
  token: string,
): Promise<RunResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(ocelBin, args, {
      cwd: spec.dir,
      env: ocelEnv(token, portFor(spec)),
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

export async function runLink(
  spec: ExampleSpec,
  token: string,
  runId: string,
): Promise<RunResult> {
  const name = `dev-${spec.framework}-${runId}`;
  const result = await runOcel(["console", "link", "--create", name], spec, token);
  if (result.code !== 0) {
    throw new Error(
      `ocel console link failed (code ${result.code})\nstdout: ${result.stdout}\nstderr: ${result.stderr}`,
    );
  }
  return result;
}

export async function clearLink(spec: ExampleSpec) {
  await rm(path.join(spec.dir, ".ocel", "console.json"), { force: true });
}

export async function prepareExample(spec: ExampleSpec): Promise<boolean> {
  if (!spec.capabilities.includes("environment")) return false;
  try {
    await writeFile(path.join(spec.dir, ".env"), nextDotenv(), { flag: "wx" });
    return true;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "EEXIST") return false;
    throw error;
  }
}

export async function clearExampleEnv(spec: ExampleSpec, created: boolean) {
  if (created) await rm(path.join(spec.dir, ".env"), { force: true });
}

export async function runMigrate(
  spec: ExampleSpec,
  token: string,
): Promise<RunResult> {
  const result = await runOcel(
    ["run", "--", "pnpm", "migrate"],
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
  output: () => string;
  stop: () => Promise<void>;
};

export function startDev(spec: ExampleSpec, token: string): DevHandle {
  const port = portFor(spec);
  const child = spawn(ocelBin, ["dev", "--", ...spec.startCmd], {
    cwd: spec.dir,
    env: ocelEnv(token, port),
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
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
        process.kill(-child.pid, "SIGTERM");
      } catch {
      }
      await delay(500);
      try {
        process.kill(-child.pid, "SIGKILL");
      } catch {
      }
    }
  };

  return { child, output: () => captured, stop };
}

export async function waitForHealth(
  spec: ExampleSpec,
  dev: DevHandle,
  timeoutMs = 90_000,
): Promise<void> {
  const url = `http://localhost:${portFor(spec)}/api/health`;
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
    }
    await delay(500);
  }
  throw new Error(`health check never became ready at ${url}`);
}

export const base = (spec: ExampleSpec) =>
  `http://localhost:${portFor(spec)}`;
