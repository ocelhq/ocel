import { type ChildProcess, execFile, spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { mkdir } from "node:fs/promises";
import net from "node:net";
import path from "node:path";
import { promisify } from "node:util";
import { repoRoot } from "../../paths";

const DISPATCH_MODULE = "./scripts/flocidispatch";
const READY_TRIES = 50;
const READY_INTERVAL_MS = 100;

const runFile = promisify(execFile);

export function dispatchArgs(
  endpoint: string,
  project: string,
  region: string,
  tasksListen: string[] = [],
): string[] {
  const args = ["-endpoint", endpoint, "-project", project, "-region", region];
  return tasksListen.length > 0 ? [...args, "-tasks-listen", tasksListen.join(",")] : args;
}

export function tasksListenAddresses(port: number, gateway: string | undefined): string[] {
  return gateway ? [`127.0.0.1:${port}`, `${gateway}:${port}`] : [`127.0.0.1:${port}`];
}

export interface RunningDispatch {
  child: ChildProcess;
  tasksEndpoint: string;
}

function pickFreePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen({ host: "127.0.0.1", port: 0 }, () => {
      const { port } = server.address() as net.AddressInfo;
      server.close(() => resolve(port));
    });
  });
}

async function readBridgeGateway(): Promise<string | undefined> {
  if (process.platform !== "linux") {
    return undefined;
  }
  try {
    const { stdout } = await runFile("docker", [
      "network",
      "inspect",
      "bridge",
      "-f",
      "{{(index .IPAM.Config 0).Gateway}}",
    ]);
    return stdout.trim() || undefined;
  } catch {
    return undefined;
  }
}

async function isAnsweringCerts(tasksEndpoint: string): Promise<boolean> {
  try {
    const response = await fetch(`${tasksEndpoint}/oauth2/v3/certs`);
    return response.status === 200;
  } catch {
    return false;
  }
}

export async function startDispatch(
  endpoint: string,
  project: string,
  region: string,
  dir: string,
): Promise<RunningDispatch> {
  await mkdir(dir, { recursive: true });
  const binary = path.join(dir, "flocidispatch");
  await runFile("go", ["build", "-o", binary, DISPATCH_MODULE], { cwd: repoRoot });
  const logFile = path.join(dir, "flocidispatch.log");
  const log = createWriteStream(logFile, { flags: "a" });
  const port = await pickFreePort();
  const tasksListen = tasksListenAddresses(port, await readBridgeGateway());
  const child = spawn(binary, dispatchArgs(endpoint, project, region, tasksListen), {
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout?.pipe(log);
  child.stderr?.pipe(log);
  const tasksEndpoint = `http://127.0.0.1:${port}`;
  for (let tries = 0; tries < READY_TRIES; tries++) {
    if (await isAnsweringCerts(tasksEndpoint)) {
      return { child, tasksEndpoint };
    }
    await new Promise((resolve) => setTimeout(resolve, READY_INTERVAL_MS));
  }
  await stopDispatch(child);
  throw new Error(
    `the tasks API of flocidispatch never answered on ${tasksEndpoint}; see ${logFile}`,
  );
}

export async function stopDispatch(child: ChildProcess | undefined): Promise<void> {
  if (!child || child.exitCode !== null || child.signalCode !== null) {
    return;
  }
  const exited = new Promise<void>((resolve) => child.once("exit", () => resolve()));
  child.kill("SIGTERM");
  await exited;
}
