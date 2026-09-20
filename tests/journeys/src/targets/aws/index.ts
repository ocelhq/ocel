import { access, rm } from "node:fs/promises";
import { setTimeout as pause } from "node:timers/promises";
import { migrates, setsEnv, setsSecret } from "../../checks";
import { INITIAL_GREETING, SECRET_TOKEN } from "../../checks/context";
import { appHostname } from "../../identity";
import type { Lane, Phase } from "../../matrix/types";
import { configTree, runOcel, treeRoot, workTree } from "../../ocel";
import type { PrepareFailures } from "../../prepare";
import type { CellUnderTest } from "../../run/cellRun";
import { migrateCommand } from "../../workspace";
import type { Deployment, ReleaseCycle, Target } from "../types";
import { AwsBootstrap } from "./bootstrap";
import { AwsDispatch } from "./dispatch";
import { ocelEnvIn } from "./namespace";
import { awaitServing } from "./serving";
import { AwsSweeper } from "./sweeper";
import { awsWorld } from "./world";

const FUNCTION_URL_BODY_BYTES = 4_500_000;

const SERVING_TIMEOUT_MS = 900_000;
const SERVING_INTERVAL_MS = 5_000;

async function cellTree(cell: CellUnderTest): Promise<string> {
  const dir = configTree(cell, "aws");
  try {
    await access(dir);
    return dir;
  } catch {
    return workTree(cell, "aws");
  }
}

export class AwsTarget implements Target, ReleaseCycle {
  readonly name = "aws";
  readonly workers = 3;
  readonly maxRequestBodyBytes = FUNCTION_URL_BODY_BYTES;
  readonly stepTimeoutMs = process.env.AWS_ENDPOINT_URL ? 600_000 : 1_800_000;

  private readonly world = awsWorld;
  private readonly bootstrap = new AwsBootstrap(this.world);
  private readonly dispatch = new AwsDispatch(this.world);
  readonly sweeper = new AwsSweeper(this.world);

  detectLane(): Promise<Lane> {
    return this.world.lane();
  }

  prepareLane(): Promise<PrepareFailures> {
    return this.bootstrap.prepareLane();
  }

  async prepareProcess(): Promise<void> {
    await this.world.settle();
  }

  async deploy(cell: CellUnderTest): Promise<Deployment> {
    const dir = await cellTree(cell);
    const env = ocelEnvIn(dir, await this.bootstrap.namespaceOf(cell));

    await this.bootstrap.bootstrapCell(cell, dir);

    if (setsEnv(cell.fixture.checks)) {
      await runOcel(
        cell,
        dir,
        "deploy",
        "env-greeting",
        ["env", "set", `GREETING=${INITIAL_GREETING}`],
        env,
      );
    }
    if (setsSecret(cell.fixture.checks)) {
      await runOcel(
        cell,
        dir,
        "deploy",
        "env-secret",
        ["env", "set", `SECRET_TOKEN=${SECRET_TOKEN}`],
        env,
      );
    }
    await runOcel(cell, dir, "deploy", "deploy", ["deploy", "--yes"], env);
    await runOcel(cell, dir, "deploy", "domain-add", ["domain", "add"], env);
    await runOcel(cell, dir, "deploy", "deploy-bound", ["deploy", "--yes"], env);

    const deployed = await this.deployment(cell);
    await this.awaitEdge(cell, "deploy", deployed);

    if (migrates(cell.fixture.checks)) {
      await runOcel(cell, dir, "deploy", "migrate", ["run", "--", ...migrateCommand()], env);
    }

    await cell.evidence.write(
      "deploy",
      "deployment.json",
      `${JSON.stringify(
        {
          slug: cell.slug,
          variant: cell.variant.name,
          apps: Object.fromEntries(cell.fixture.apps.map((app) => [app, deployed.baseUrl(app)])),
        },
        null,
        2,
      )}\n`,
    );
    return deployed;
  }

  async redeploy(cell: CellUnderTest, greeting: string): Promise<Deployment> {
    const dir = await cellTree(cell);
    const env = ocelEnvIn(dir, await this.bootstrap.namespaceOf(cell));
    if (setsEnv(cell.fixture.checks)) {
      await runOcel(
        cell,
        dir,
        "redeploy",
        "env-greeting",
        ["env", "set", `GREETING=${greeting}`],
        env,
      );
    }
    await runOcel(cell, dir, "redeploy", "deploy", ["deploy", "--yes"], env);
    const deployed = await this.deployment(cell);
    await this.awaitEdge(cell, "redeploy", deployed);
    return deployed;
  }

  async rollback(cell: CellUnderTest): Promise<Deployment> {
    const dir = await cellTree(cell);
    const env = ocelEnvIn(dir, await this.bootstrap.namespaceOf(cell));
    await runOcel(cell, dir, "rollback", "rollback", ["rollback"], env);
    const deployed = await this.deployment(cell);
    await this.awaitEdge(cell, "rollback", deployed);
    return deployed;
  }

  async destroy(cell: CellUnderTest): Promise<void> {
    const namespace = await this.bootstrap.namespaceOf(cell);
    const hosts = this.hostnames(cell);
    const unbound: string[] = [];
    let dir: string | undefined;
    try {
      dir = await cellTree(cell);
      const env = ocelEnvIn(dir, namespace);
      for (const [app, host] of hosts) {
        try {
          await runOcel(cell, dir, "destroy", `domain-rm-${app}`, ["domain", "rm", host], env);
        } catch (error) {
          unbound.push(error instanceof Error ? error.message : String(error));
        }
      }
      await runOcel(cell, dir, "destroy", "destroy", ["destroy", "production", "--yes"], env);
      if (unbound.length > 0 && (await this.sweeper.exists(cell.slug))) {
        throw new Error(unbound.join("\n"));
      }
    } finally {
      if (dir && namespace) {
        await this.bootstrap.destroyCellBootstrap(cell, dir, namespace);
      }
      await rm(treeRoot(cell, "aws"), { recursive: true, force: true });
    }
  }

  private hostnames(cell: CellUnderTest): Map<string, string> {
    const zone = this.world.zone();
    return new Map(
      cell.fixture.apps.map((app) => {
        const host = appHostname(app, cell.slug, zone);
        if (!host) {
          throw new Error(`${cell.slug} declares no hostname for ${app}`);
        }
        return [app, host];
      }),
    );
  }

  private async deployment(cell: CellUnderTest): Promise<Deployment> {
    const dispatch = await this.dispatch.fetch();
    const hosts = this.hostnames(cell);
    return {
      baseUrl: (app) => {
        const host = hosts.get(app);
        if (!host) {
          throw new Error(`${cell.name} has no app named ${app} on aws`);
        }
        return `https://${host}`;
      },
      fetch: dispatch,
    };
  }

  private async awaitEdge(cell: CellUnderTest, phase: Phase, deployed: Deployment): Promise<void> {
    if (!(await this.world.real())) {
      return;
    }
    const urls = new Map(cell.fixture.apps.map((app) => [app, deployed.baseUrl(app)]));
    const served = await awaitServing(deployed.fetch, urls, {
      timeoutMs: SERVING_TIMEOUT_MS,
      intervalMs: SERVING_INTERVAL_MS,
      now: () => Date.now(),
      sleep: (ms) => pause(ms),
    });
    await cell.evidence.write(phase, "serving.json", `${JSON.stringify(served, null, 2)}\n`);
  }
}
