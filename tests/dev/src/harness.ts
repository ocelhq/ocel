import { type ChildProcess, spawn } from "node:child_process";
import { rm } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const here = path.dirname(fileURLToPath(import.meta.url));
export const repoRoot = path.resolve(here, "..", "..", "..");
export const examplesDir = path.join(repoRoot, "examples");

export const ocelBin =
  process.env.OCEL_BIN ?? path.join(repoRoot, "cli", "bin", "ocel");

export const apiUrl = process.env.OCEL_API_URL ?? "http://localhost:3000";

export type BlobSpec = {
  uploadPath: string;
  documentsPath: string;
  uploaderName: string;
  input: Record<string, unknown>;
  file: { name: string; type: string };
  expectedKeyIncludes: string[];
  expectedOwnerId: string;
};

export type ExampleSpec = {
  framework: "next" | "express" | "hono";
  dir: string;
  port: number;
  healthPath: string;
  todosPath: string;
  migrateCmd: string[];
  startCmd: string[];
  blob: BlobSpec;
};

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
    port: 3101,
    healthPath: "/api/health",
    todosPath: "/api/todos",
    migrateCmd: ["pnpm", "migrate"],
    startCmd: ["pnpm", "start"],
    blob: {
      uploadPath: "/api/upload",
      documentsPath: "/api/documents",
      uploaderName: "avatar",
      input: { userId: "user-1" },
      file: { name: "me.png", type: "image/png" },
      expectedKeyIncludes: ["avatars/", "me.png"],
      expectedOwnerId: "user-1",
    },
  },
  express: {
    framework: "express",
    dir: path.join(examplesDir, "express"),
    port: 3102,
    healthPath: "/health",
    todosPath: "/todos",
    migrateCmd: ["pnpm", "migrate"],
    startCmd: ["pnpm", "start"],
    blob: {
      uploadPath: "/api/upload",
      documentsPath: "/documents",
      uploaderName: "document",
      input: { ownerId: "owner-1" },
      file: { name: "report.pdf", type: "application/pdf" },
      expectedKeyIncludes: ["documents/", "report.pdf"],
      expectedOwnerId: "owner-1",
    },
  },
  hono: {
    framework: "hono",
    dir: path.join(examplesDir, "hono"),
    port: 3103,
    healthPath: "/health",
    todosPath: "/todos",
    migrateCmd: ["pnpm", "migrate"],
    startCmd: ["pnpm", "start"],
    blob: {
      uploadPath: "/api/upload",
      documentsPath: "/documents",
      uploaderName: "attachment",
      input: { threadId: "thread-1" },
      file: { name: "note.png", type: "image/png" },
      expectedKeyIncludes: ["threads/thread-1/", "note.png"],
      expectedOwnerId: "thread-1",
    },
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

export async function runLink(
  spec: ExampleSpec,
  token: string,
  runId: string,
): Promise<RunResult> {
  const name = `dev-${spec.framework}-${runId}`;
  const result = await runOcel(["link", "--create", name], spec, token);
  if (result.code !== 0) {
    throw new Error(
      `ocel link failed (code ${result.code})\nstdout: ${result.stdout}\nstderr: ${result.stderr}`,
    );
  }
  return result;
}

export async function clearLink(spec: ExampleSpec) {
  await rm(path.join(spec.dir, ".ocel", "link.json"), { force: true });
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
  output: () => string;
  stop: () => Promise<void>;
};

export function startDev(spec: ExampleSpec, token: string): DevHandle {
  const child = spawn(ocelBin, ["dev", "--", ...spec.startCmd], {
    cwd: spec.dir,
    env: ocelEnv(token, spec.port),
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
    }
    await delay(500);
  }
  throw new Error(`health check never became ready at ${url}`);
}

export const base = (spec: ExampleSpec) => `http://localhost:${spec.port}`;
