import { type ChildProcess, execFile, spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { repoRoot } from "../../paths";

const DISPATCH_MODULE = "./scripts/flocidispatch";
const STARTUP_GRACE_MS = 500;

const runFile = promisify(execFile);

export function dispatchArgs(endpoint: string, project: string, region: string): string[] {
  return ["-endpoint", endpoint, "-project", project, "-region", region];
}

export async function startDispatch(
  endpoint: string,
  project: string,
  region: string,
  dir: string,
): Promise<ChildProcess> {
  await mkdir(dir, { recursive: true });
  const binary = path.join(dir, "flocidispatch");
  await runFile("go", ["build", "-o", binary, DISPATCH_MODULE], { cwd: repoRoot });
  const logFile = path.join(dir, "flocidispatch.log");
  const log = createWriteStream(logFile, { flags: "a" });
  const child = spawn(binary, dispatchArgs(endpoint, project, region), {
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout?.pipe(log);
  child.stderr?.pipe(log);
  const exited = new Promise<number | null>((resolve) => child.once("exit", resolve));
  const grace = new Promise<"running">((resolve) =>
    setTimeout(() => resolve("running"), STARTUP_GRACE_MS),
  );
  const started = await Promise.race([exited, grace]);
  if (started !== "running") {
    throw new Error(`flocidispatch exited with ${started} on start; see ${logFile}`);
  }
  return child;
}

export async function stopDispatch(child: ChildProcess | undefined): Promise<void> {
  if (!child || child.exitCode !== null || child.signalCode !== null) {
    return;
  }
  const exited = new Promise<void>((resolve) => child.once("exit", () => resolve()));
  child.kill("SIGTERM");
  await exited;
}
