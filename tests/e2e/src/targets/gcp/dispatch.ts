import { type ChildProcess, execFile, spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { repoRoot } from "../../paths";

const DISPATCH_MODULE = "./scripts/flocidispatch";

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
  const log = createWriteStream(path.join(dir, "flocidispatch.log"), { flags: "a" });
  const child = spawn(binary, dispatchArgs(endpoint, project, region), {
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout?.pipe(log);
  child.stderr?.pipe(log);
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
