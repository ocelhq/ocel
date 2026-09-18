import path from "node:path";
import { applyConsoleEnvDefaults, consoleUrl, HARNESS_ONLY_ENV } from "@ocel-tests/shared/env";
import { migrates, setsEnv } from "../checks";
import { INITIAL_GREETING, SECRET_TOKEN } from "../checks/context";
import { journeyConfigIn } from "../config";
import { isStranded } from "../identity";
import type { Lane } from "../matrix/types";
import { runOcel } from "../ocel";
import type { CellUnderTest } from "../run/cellRun";
import { appCommand, migrateCommand } from "../workspace";
import { LocalDevTarget } from "./localDev";
import type { Sweeper } from "./types";

const START_CONSOLE = [
  "docker compose up -d postgres ocel-cloud minio",
  "pnpm --filter @console/db db:push",
  "pnpm --filter @console/web dev",
].join(" && ");

type ConsoleProject = { id: string; slug: string };

export class DevTarget extends LocalDevTarget {
  readonly name = "dev";

  private token: Promise<string> | undefined;

  readonly sweeper: Sweeper = {
    list: () => this.projectSlugs(),
    exists: async (slug) => (await this.projectSlugs()).includes(slug),
    sweepStale: (runId) => this.sweepStale(runId),
    sweepRun: async () => {},
  };

  async detectLane(): Promise<Lane> {
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
      throw because(
        `answered ${res.status}, and the console answers an unauthorized list with 401`,
      );
    }
    return "dev";
  }

  async prepareProcess(): Promise<void> {
    await this.detectLane();
    await this.accessToken();
  }

  protected async ocelEnv(dir: string): Promise<NodeJS.ProcessEnv> {
    const env: NodeJS.ProcessEnv = {
      ...process.env,
      OCEL_ACCESS_TOKEN: await this.accessToken(),
      OCEL_CONSOLE_URL: consoleUrl(),
    };
    for (const name of HARNESS_ONLY_ENV) {
      delete env[name];
    }
    return { ...env, OCEL_CONFIG: path.join(dir, journeyConfigIn(dir)) };
  }

  protected async beforeServing(
    cell: CellUnderTest,
    dir: string,
    env: NodeJS.ProcessEnv,
  ): Promise<void> {
    await runOcel(cell, dir, "deploy", "console-link", ["link", "--create", cell.slug], env);
    if (setsEnv(cell.fixture.checks)) {
      await runOcel(
        cell,
        dir,
        "deploy",
        "env-greeting",
        ["env", "set", `GREETING=${INITIAL_GREETING}`, "--dev"],
        env,
      );
      await runOcel(
        cell,
        dir,
        "deploy",
        "env-secret",
        ["env", "set", `SECRET_TOKEN=${SECRET_TOKEN}`, "--dev"],
        env,
      );
    }
    if (migrates(cell.fixture.checks)) {
      await runOcel(cell, dir, "deploy", "migrate", ["run", "--", ...migrateCommand()], env);
    }
  }

  protected serveArgs(cell: CellUnderTest, app: string): string[] {
    return ["dev", "--", ...appCommand(cell.fixture, app)];
  }

  protected async afterStopping(cell: CellUnderTest): Promise<void> {
    const project = (await this.consoleProjects()).find((found) => found.slug === cell.slug);
    if (project) {
      await this.deleteConsoleProject(project);
    }
  }

  private accessToken(): Promise<string> {
    this.token ??= (async () => {
      applyConsoleEnvDefaults();
      const existing = process.env.OCEL_ACCESS_TOKEN;
      if (existing) {
        return existing;
      }
      const { seed } = await import("@ocel-tests/shared/seed");
      return (await seed("Journey")).token;
    })();
    return this.token;
  }

  private async consoleProjects(): Promise<ConsoleProject[]> {
    const res = await fetch(`${consoleUrl()}/api/projects`, {
      headers: { Authorization: `Bearer ${await this.accessToken()}` },
    });
    if (!res.ok) {
      throw new Error(`the console answered ${res.status} listing this account's projects`);
    }
    return (await res.json()) as ConsoleProject[];
  }

  private async projectSlugs(): Promise<string[]> {
    return (await this.consoleProjects()).map((project) => project.slug);
  }

  private async deleteConsoleProject(project: ConsoleProject): Promise<void> {
    const res = await fetch(`${consoleUrl()}/api/projects/${project.id}`, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${await this.accessToken()}` },
    });
    if (!res.ok && res.status !== 404) {
      throw new Error(`the console answered ${res.status} deleting the project ${project.slug}`);
    }
  }

  private async sweepStale(runId: string): Promise<void> {
    const projects = await this.consoleProjects();
    const stranded = projects.filter((project) => isStranded(project.slug, runId));
    for (const project of stranded) {
      await this.deleteConsoleProject(project);
    }

    const left = new Set(await this.projectSlugs());
    const standing = stranded.filter((project) => left.has(project.slug)).map(({ slug }) => slug);
    if (standing.length > 0) {
      throw new Error(
        `the dev sweep deleted ${standing.join(", ")} and the console still lists them`,
      );
    }
  }
}
