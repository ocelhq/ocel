import { writeFile } from "node:fs/promises";
import path from "node:path";
import { HARNESS_ONLY_ENV, localPostgresUrl, postgresBinding } from "@ocel-tests/shared/env";
import { SQL } from "bun";
import { migrates, setsEnv } from "../checks";
import { INITIAL_GREETING, SECRET_TOKEN } from "../checks/context";
import { journeyConfigIn } from "../config";
import { HARNESS_PREFIX, isStranded } from "../identity";
import type { Lane } from "../matrix/types";
import { runOcel } from "../ocel";
import type { CellUnderTest } from "../run/cellRun";
import { appCommand, migrateCommand } from "../workspace";
import { LocalDevTarget } from "./localDev";
import type { Sweeper } from "./types";

const DOTFILE = ".env";

const START_POSTGRES = "docker compose up -d ocel-cloud";

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

async function writeDotfile(cell: CellUnderTest, dir: string): Promise<void> {
  const lines: string[] = [];
  if (setsEnv(cell.fixture.checks)) {
    lines.push(`GREETING=${INITIAL_GREETING}`, `SECRET_TOKEN=${SECRET_TOKEN}`);
  }
  if (migrates(cell.fixture.checks)) {
    lines.push(
      `OCEL_RESOURCE_POSTGRES_main=${postgresBinding("main", await freshDatabase(cell.slug))}`,
    );
  }
  await writeFile(path.join(dir, DOTFILE), `${lines.join("\n")}\n`, "utf8");
  await cell.evidence.write("deploy", DOTFILE, `${lines.join("\n")}\n`);
}

async function journeyDatabases(): Promise<string[]> {
  return withAdmin(async (sql) => {
    const rows = (await sql`SELECT datname FROM pg_database`) as Array<{ datname: string }>;
    return journeySlugs(rows.map((row) => row.datname));
  });
}

export class DevLocalTarget extends LocalDevTarget {
  readonly name = "dev-local";

  readonly sweeper: Sweeper = {
    list: () => this.stillServing(),
    exists: async (slug) => (await this.stillServing()).includes(slug),
    sweepStale: async (runId) => {
      for (const slug of await journeyDatabases()) {
        if (isStranded(slug, runId)) {
          await dropDatabase(slug);
        }
      }
    },
    sweepRun: async () => {},
  };

  async detectLane(): Promise<Lane> {
    return "dev-local";
  }

  async prepareProcess(): Promise<void> {}

  protected async ocelEnv(dir: string): Promise<NodeJS.ProcessEnv> {
    const env: NodeJS.ProcessEnv = { ...process.env };
    for (const name of [...HARNESS_ONLY_ENV, "OCEL_ACCESS_TOKEN", "OCEL_CONSOLE_URL"]) {
      delete env[name];
    }
    return { ...env, OCEL_CONFIG: path.join(dir, journeyConfigIn(dir)) };
  }

  protected async beforeServing(
    cell: CellUnderTest,
    dir: string,
    env: NodeJS.ProcessEnv,
  ): Promise<void> {
    await writeDotfile(cell, dir);
    if (migrates(cell.fixture.checks)) {
      await runOcel(
        cell,
        dir,
        "deploy",
        "migrate",
        ["run", "--local", "--", ...migrateCommand()],
        env,
      );
    }
  }

  protected serveArgs(cell: CellUnderTest, app: string): string[] {
    return ["dev", "--local", "--", ...appCommand(cell.fixture, app)];
  }

  protected async afterStopping(cell: CellUnderTest): Promise<void> {
    if (migrates(cell.fixture.checks)) {
      await dropDatabase(cell.slug);
    }
  }
}
