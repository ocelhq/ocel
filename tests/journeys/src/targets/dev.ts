import { rm } from "node:fs/promises";
import path from "node:path";
import { applyConsoleEnvDefaults, consoleUrl, HARNESS_ONLY_ENV } from "@ocel-tests/shared/env";
import { journeyConfigIn } from "../config";
import { INITIAL_GREETING, SECRET_TOKEN, UNCAPPED_BODY_BYTES } from "../contract";
import type { ExpectationEnvironment } from "../expectations/types";
import { isStranded } from "../identity";
import { runOcel, treeRoot, workTree } from "../ocel";
import { migrates, setsEnv } from "../rows";
import { appCommand, migrateCommand } from "../workspace";
import { baseUrls, type Standing, serve, stateStaysHome, stopStanding } from "./devShared";
import type { CellContext, Deployment, Target } from "./types";

const START_CONSOLE = [
  "docker compose up -d postgres ocel-cloud minio",
  "pnpm --filter @console/db db:push",
  "pnpm --filter @console/web dev",
].join(" && ");

const running = new Map<string, Standing>();

let seeded: Promise<string> | undefined;

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

function childEnv(token: string): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    OCEL_ACCESS_TOKEN: token,
    OCEL_API_URL: consoleUrl(),
  };
  for (const name of HARNESS_ONLY_ENV) {
    delete env[name];
  }
  return env;
}

async function up(cell: CellContext): Promise<Deployment> {
  const token = await accessToken();
  const dir = await workTree(cell, "dev");
  const env = { ...childEnv(token), OCEL_CONFIG: path.join(dir, journeyConfigIn(dir)) };

  await runOcel(cell, dir, "up", "console-link", ["console", "link", "--create", cell.slug], env);
  if (setsEnv(cell.fixture.rows)) {
    await runOcel(
      cell,
      dir,
      "up",
      "env-greeting",
      ["env", "set", `GREETING=${INITIAL_GREETING}`, "--dev"],
      env,
    );
    await runOcel(
      cell,
      dir,
      "up",
      "env-secret",
      ["env", "set", `SECRET_TOKEN=${SECRET_TOKEN}`, "--dev"],
      env,
    );
  }
  if (migrates(cell.fixture.rows)) {
    await runOcel(cell, dir, "up", "migrate", ["run", "--", ...migrateCommand()], env);
  }

  const standing: Standing = { dir, apps: [] };
  running.set(cell.slug, standing);
  const urls = new Map<string, string>();
  for (const app of cell.fixture.apps) {
    const handle = await serve(cell, dir, env, app, [
      "dev",
      "--",
      ...appCommand(cell.fixture, app),
    ]);
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
    baseUrl: baseUrls(cell, urls, "dev"),
    fetch: (...args) => fetch(...args),
  };
}

async function destroy(cell: CellContext): Promise<void> {
  const standing = running.get(cell.slug);
  if (standing) {
    await stopStanding(cell, standing);
    await rm(treeRoot(cell, "dev"), { recursive: true, force: true });
    running.delete(cell.slug);
  }
  const project = (await consoleProjects()).find((found) => found.slug === cell.slug);
  if (project) {
    await deleteConsoleProject(project);
  }
}

type ConsoleProject = { id: string; slug: string };

async function consoleProjects(): Promise<ConsoleProject[]> {
  const token = await accessToken();
  const res = await fetch(`${consoleUrl()}/api/projects`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) {
    throw new Error(`the console answered ${res.status} listing this account's projects`);
  }
  return (await res.json()) as ConsoleProject[];
}

async function deleteConsoleProject(project: ConsoleProject): Promise<void> {
  const token = await accessToken();
  const res = await fetch(`${consoleUrl()}/api/projects/${project.id}`, {
    method: "DELETE",
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok && res.status !== 404) {
    throw new Error(`the console answered ${res.status} deleting the project ${project.slug}`);
  }
}

async function sweep(runId: string): Promise<void> {
  const projects = await consoleProjects();
  const stranded = projects.filter((project) => isStranded(project.slug, runId));
  for (const project of stranded) {
    await deleteConsoleProject(project);
  }

  const left = new Set(await list());
  const standing = stranded.filter((project) => left.has(project.slug)).map(({ slug }) => slug);
  if (standing.length > 0) {
    throw new Error(
      `the dev sweep deleted ${standing.join(", ")} and the console still lists them`,
    );
  }
}

async function list(): Promise<string[]> {
  return (await consoleProjects()).map((project) => project.slug);
}

export const devTarget: Target = {
  name: "dev",
  concurrency: 4,
  largeBodyBytes: UNCAPPED_BODY_BYTES,
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
  sweep,
  sweepOwn: async () => {},
};
