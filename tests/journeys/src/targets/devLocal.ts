import { rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { HARNESS_ONLY_ENV, localPostgresUrl, postgresBinding } from "@ocel-tests/shared/env";
import { SQL } from "bun";
import { journeyConfigIn } from "../config";
import { INITIAL_GREETING, SECRET_TOKEN, UNCAPPED_BODY_BYTES } from "../contract";
import type { ExpectationEnvironment } from "../expectations/types";
import { HARNESS_PREFIX, isStranded } from "../identity";
import { runOcel, treeRoot, workTree } from "../ocel";
import { migrates, setsEnv } from "../rows";
import { appCommand, migrateCommand } from "../workspace";
import {
  baseUrls,
  type Standing,
  serve,
  stateStaysHome,
  stillServing,
  stopStanding,
} from "./devShared";
import type { CellContext, Deployment, Target } from "./types";

const TARGET = "dev-local";

const DOTFILE = ".env";

const START_POSTGRES = "docker compose up -d ocel-cloud";

const running = new Map<string, Standing>();

const LONGEST_IDENTIFIER = 63;

const NAMEABLE = /^[A-Za-z0-9_.-]+$/;

export function databaseName(slug: string): string {
  if (!NAMEABLE.test(slug)) {
    throw new Error(
      `${slug} holds a character a quoted postgres identifier may not carry; a cell database is named by its slug alone.`,
    );
  }
  if (slug.length > LONGEST_IDENTIFIER) {
    throw new Error(
      `${slug} is ${slug.length} characters, and a postgres identifier holds ${LONGEST_IDENTIFIER}. Shorten the cell name behind it.`,
    );
  }
  return slug;
}

export function journeySlugs(names: string[]): string[] {
  return names.filter((name) => name.startsWith(HARNESS_PREFIX));
}

async function withAdmin<T>(work: (sql: SQL) => Promise<T>): Promise<T> {
  const url = localPostgresUrl();
  const sql = new SQL(url);
  try {
    return await work(sql);
  } catch (error) {
    throw new Error(
      `${url} is not answering as the postgres a local run resolves against (${String(error)}); the journey harness never starts it. Run: ${START_POSTGRES}`,
    );
  } finally {
    await sql.end();
  }
}

async function freshDatabase(slug: string): Promise<string> {
  const name = databaseName(slug);
  await withAdmin(async (sql) => {
    await sql.unsafe(`DROP DATABASE IF EXISTS "${name}" WITH (FORCE)`);
    await sql.unsafe(`CREATE DATABASE "${name}"`);
  });
  const url = new URL(localPostgresUrl());
  url.pathname = `/${name}`;
  return url.toString();
}

async function dropDatabase(slug: string): Promise<void> {
  await withAdmin(async (sql) => {
    await sql.unsafe(`DROP DATABASE IF EXISTS "${databaseName(slug)}" WITH (FORCE)`);
  });
}

async function writeDotfile(cell: CellContext, dir: string): Promise<void> {
  const lines: string[] = [];
  if (setsEnv(cell.fixture.rows)) {
    lines.push(`GREETING=${INITIAL_GREETING}`, `SECRET_TOKEN=${SECRET_TOKEN}`);
  }
  if (migrates(cell.fixture.rows)) {
    lines.push(
      `OCEL_RESOURCE_POSTGRES_main=${postgresBinding("main", await freshDatabase(cell.slug))}`,
    );
  }
  await writeFile(path.join(dir, DOTFILE), `${lines.join("\n")}\n`, "utf8");
  await cell.evidence.write("up", DOTFILE, `${lines.join("\n")}\n`);
}

function childEnv(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env };
  for (const name of [...HARNESS_ONLY_ENV, "OCEL_ACCESS_TOKEN", "OCEL_API_URL"]) {
    delete env[name];
  }
  return env;
}

async function up(cell: CellContext): Promise<Deployment> {
  const dir = await workTree(cell, TARGET);
  const env = { ...childEnv(), OCEL_CONFIG: path.join(dir, journeyConfigIn(dir)) };

  await writeDotfile(cell, dir);
  if (migrates(cell.fixture.rows)) {
    await runOcel(cell, dir, "up", "migrate", ["run", "--local", "--", ...migrateCommand()], env);
  }

  const standing: Standing = { dir, apps: [] };
  running.set(cell.slug, standing);
  const urls = new Map<string, string>();
  for (const app of cell.fixture.apps) {
    const handle = await serve(cell, dir, env, app, [
      "dev",
      "--local",
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
    baseUrl: baseUrls(cell, urls, TARGET),
    fetch: (...args) => fetch(...args),
  };
}

async function destroy(cell: CellContext): Promise<void> {
  const standing = running.get(cell.slug);
  if (standing) {
    await stopStanding(cell, standing);
    await rm(treeRoot(cell, TARGET), { recursive: true, force: true });
    running.delete(cell.slug);
  }
  if (migrates(cell.fixture.rows)) {
    await dropDatabase(cell.slug);
  }
}

async function journeyDatabases(): Promise<string[]> {
  return withAdmin(async (sql) => {
    const rows = (await sql`SELECT datname FROM pg_database`) as Array<{ datname: string }>;
    return journeySlugs(rows.map((row) => row.datname));
  });
}

async function sweep(runId: string): Promise<void> {
  for (const slug of await journeyDatabases()) {
    if (isStranded(slug, runId)) {
      await dropDatabase(slug);
    }
  }
}

async function list(): Promise<string[]> {
  return stillServing(running);
}

export const devLocalTarget: Target = {
  name: TARGET,
  concurrency: 4,
  largeBodyBytes: UNCAPPED_BODY_BYTES,
  legTimeoutMs: 180_000,
  legs: ["up", "contract", "destroy"],
  guard: async (): Promise<ExpectationEnvironment> => TARGET,
  setup: async () => {},
  up,
  destroy,
  list,
  stands: async (slug) => (await list()).includes(slug),
  sweep,
  sweepOwn: async () => {},
};
