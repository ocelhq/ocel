import { type ChildProcess, spawn } from "node:child_process";
import { rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { apiUrl, nextDotenv } from "../env";
import { repoRoot } from "../examples";
import { successful } from "../process";
import type { Example, Target } from "../types";

const ocelBin = process.env.OCEL_BIN ?? path.join(repoRoot, "cli", "bin", "ocel");

const ports: Record<Example["name"], number> = {
  next: 3101,
  express: 3102,
  hono: 3103,
  fastify: 3104,
  "with-transforms": 3106,
};

function env(token: string, example: Example): NodeJS.ProcessEnv {
  return {
    ...process.env,
    OCEL_ACCESS_TOKEN: token,
    OCEL_API_URL: apiUrl,
    PORT: String(ports[example.name]),
  };
}

async function prepare(example: Example) {
  let createdEnv = false;
  if (example.capabilities.includes("env")) {
    try {
      await writeFile(path.join(example.dir, ".env"), nextDotenv(), {
        flag: "wx",
      });
      createdEnv = true;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
    }
  }
  return createdEnv;
}

async function stop(child: ChildProcess) {
  if (!child.pid || child.exitCode !== null) return;
  try {
    process.kill(-child.pid, "SIGTERM");
  } catch {}
  await delay(500);
  try {
    process.kill(-child.pid, "SIGKILL");
  } catch {}
}

async function waitForHealth(
  example: Example,
  child: ChildProcess,
  output: () => string,
) {
  const url = `http://localhost:${ports[example.name]}/api/health`;
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new Error(
        `ocel dev for ${example.name} exited before ${url} became ready:\n${output()}`,
      );
    }
    try {
      if ((await fetch(url)).ok) return;
    } catch {}
    await delay(500);
  }
  throw new Error(`health check never became ready at ${url}`);
}

export function createDevTarget(token: string): Target {
  return {
    name: "dev",
    async up(example) {
      const childEnv = env(token, example);
      const createdEnv = await prepare(example);
      let child: ChildProcess | undefined;
      try {
        await successful(
          "ocel console link",
          ocelBin,
          [
            "console",
            "link",
            "--create",
            `dev-${example.name}-${crypto.randomUUID()}`,
          ],
          { cwd: example.dir, env: childEnv },
        );
        await successful(
          "ocel run migrate",
          ocelBin,
          ["run", "--", "pnpm", "migrate"],
          { cwd: example.dir, env: childEnv },
        );
        child = spawn(ocelBin, ["dev", "--", ...example.startCmd], {
          cwd: example.dir,
          env: childEnv,
          detached: true,
          stdio: ["ignore", "pipe", "pipe"],
        });
        let captured = "";
        const capture = (data: Buffer) => {
          captured += data.toString();
          process.stderr.write(`[${example.name} dev] ${data}`);
        };
        child.stdout?.on("data", capture);
        child.stderr?.on("data", capture);
        await waitForHealth(example, child, () => captured);
        return {
          baseUrl: `http://localhost:${ports[example.name]}`,
          output: () => captured,
          teardown: async () => {
            await stop(child!);
            await rm(path.join(example.dir, ".ocel", "console.json"), {
              force: true,
            });
            if (createdEnv) {
              await rm(path.join(example.dir, ".env"), { force: true });
            }
          },
        };
      } catch (error) {
        if (child) await stop(child);
        await rm(path.join(example.dir, ".ocel", "console.json"), {
          force: true,
        });
        if (createdEnv) {
          await rm(path.join(example.dir, ".env"), { force: true });
        }
        throw error;
      }
    },
  };
}
