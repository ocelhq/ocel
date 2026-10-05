import { access, rm } from "node:fs/promises";
import { setTimeout as pause } from "node:timers/promises";
import { migrates, setsEnv, setsSecret } from "../../checks";
import {
  INITIAL_GREETING,
  JOURNEY_NONCE_ENV,
  SECRET_TOKEN,
  setsJourneyNonce,
} from "../../checks/context";
import { appHostname } from "../../identity";
import type { Lane, Phase } from "../../matrix/types";
import { sanitize } from "../../naming";
import { configTree, type Ran, recordOutput, runOcel, treeRoot, workTree } from "../../ocel";
import type { PrepareFailures } from "../../prepare";
import type { CellUnderTest } from "../../run/cellRun";
import { migrateCommand } from "../../workspace";
import type { Deployment, Exposure, ReleaseCycle, Restart, Target } from "../types";
import { bootstrapBuild, deployOverOlderBootstrap } from "../upgrade";
import { AwsBootstrap } from "./bootstrap";
import { AwsDispatch } from "./dispatch";
import { describeExposed, failOver, keepPersistedGroups, listTaggedGroups } from "./kv";
import { ocelEnvIn } from "./namespace";
import { awaitServing } from "./serving";
import { cliAt } from "./store";
import { AwsSweeper } from "./sweeper";
import { awsWorld } from "./world";

const FUNCTION_URL_BODY_BYTES = 4_500_000;

const SERVING_TIMEOUT_MS = 900_000;
const SERVING_INTERVAL_MS = 5_000;

const FAILOVER_TIMEOUT_MS = 1_200_000;
const FAILOVER_INTERVAL_MS = 15_000;

async function cellTree(cell: CellUnderTest): Promise<string> {
  const dir = configTree(cell, "aws");
  try {
    await access(dir);
    return dir;
  } catch {
    return workTree(cell, "aws");
  }
}

export class AwsTarget implements Target, ReleaseCycle, Restart, Exposure {
  readonly name = "aws";
  readonly workers = 3;
  readonly maxRequestBodyBytes = FUNCTION_URL_BODY_BYTES;
  readonly stepTimeoutMs = process.env.AWS_ENDPOINT_URL ? 600_000 : 1_800_000;

  private readonly world = awsWorld;
  private readonly bootstrap = new AwsBootstrap(this.world);
  private readonly dispatch = new AwsDispatch(this.world);
  readonly sweeper = new AwsSweeper(this.world);
  private readonly said = new Map<string, string[]>();

  detectLane(): Promise<Lane> {
    return this.world.lane();
  }

  prepareLane(): Promise<PrepareFailures> {
    return this.bootstrap.prepareLane();
  }

  async prepareProcess(): Promise<void> {
    await this.world.detect();
  }

  async deploy(cell: CellUnderTest): Promise<Deployment> {
    const dir = await cellTree(cell);
    const env = ocelEnvIn(dir, await this.bootstrap.namespaceOf(cell));

    await this.bootstrap.bootstrapCell(cell, dir);

    if (setsEnv(cell.fixture.checks)) {
      await this.run(
        cell,
        dir,
        "deploy",
        "env-greeting",
        ["env", "set", `GREETING=${INITIAL_GREETING}`],
        env,
      );
    }
    if (setsSecret(cell.fixture.checks)) {
      await this.run(
        cell,
        dir,
        "deploy",
        "env-secret",
        ["env", "set", `SECRET_TOKEN=${SECRET_TOKEN}`],
        env,
      );
    }
    if (setsJourneyNonce(cell.fixture.checks)) {
      await this.run(
        cell,
        dir,
        "deploy",
        "env-journey-nonce",
        ["env", "set", `${JOURNEY_NONCE_ENV}=${cell.journeyNonce}`],
        env,
      );
    }
    if (bootstrapBuild(process.env)) {
      const outcome = await deployOverOlderBootstrap({
        deploy: (name) => this.run(cell, dir, "deploy", name, ["deploy", "--yes"], env),
        bootstrap: () => this.bootstrap.rebootstrapCell(cell, dir),
        projectExists: () => this.sweeper.exists(cell.slug),
      });
      await cell.evidence.write("deploy", "upgrade.txt", `${outcome}\n`);
    } else {
      await this.run(cell, dir, "deploy", "deploy", ["deploy", "--yes"], env);
    }
    await this.run(cell, dir, "deploy", "domain-add", ["domain", "add"], env);

    const deployed = await this.deployment(cell);
    await this.awaitEdge(cell, "deploy", deployed);

    if (migrates(cell.fixture.checks)) {
      await this.run(cell, dir, "deploy", "migrate", ["run", "--", ...migrateCommand()], env);
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
      await this.run(
        cell,
        dir,
        "redeploy",
        "env-greeting",
        ["env", "set", `GREETING=${greeting}`],
        env,
      );
    }
    await this.run(cell, dir, "redeploy", "deploy", ["deploy", "--yes"], env);
    const deployed = await this.deployment(cell);
    await this.awaitEdge(cell, "redeploy", deployed);
    return deployed;
  }

  async rollback(cell: CellUnderTest): Promise<Deployment> {
    const dir = await cellTree(cell);
    const env = ocelEnvIn(dir, await this.bootstrap.namespaceOf(cell));
    await this.run(cell, dir, "rollback", "rollback", ["rollback", "--yes"], env);
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
          await this.run(
            cell,
            dir,
            "destroy",
            `domain-rm-${app}`,
            ["domain", "rm", host, "--yes"],
            env,
          );
        } catch (error) {
          unbound.push(error instanceof Error ? error.message : String(error));
        }
      }
      await this.run(cell, dir, "destroy", "destroy", ["destroy", "production", "--yes"], env);
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

  async restart(cell: CellUnderTest): Promise<Deployment> {
    const cli = cliAt(await this.world.endpoint());
    const groups = keepPersistedGroups(await listTaggedGroups(cli, sanitize(cell.slug)));
    if (groups.length === 0) {
      throw new Error(
        `no replication group tagged ocel:project=${sanitize(cell.slug)} has an ocel:resource tag naming a store the persistence check reads, so nothing ${cell.name} declared was restarted`,
      );
    }
    const failedOver: string[] = [];
    for (const group of groups) {
      failedOver.push(
        await failOver(cli, group.id, {
          timeoutMs: FAILOVER_TIMEOUT_MS,
          intervalMs: FAILOVER_INTERVAL_MS,
          now: () => Date.now(),
          sleep: (ms) => pause(ms),
        }),
      );
    }
    await cell.evidence.write("restart", "failover.txt", `${failedOver.join("\n")}\n`);
    return this.deployment(cell);
  }

  async readExposed(cell: CellUnderTest): Promise<string> {
    const shown = await describeExposed(cliAt(await this.world.endpoint()), sanitize(cell.slug));
    return [...(this.said.get(cell.slug) ?? []), shown].join("\n");
  }

  private run(
    cell: CellUnderTest,
    dir: string,
    phase: Phase,
    name: string,
    args: string[],
    env: NodeJS.ProcessEnv,
  ): Promise<Ran> {
    let said = this.said.get(cell.slug);
    if (!said) {
      said = [];
      this.said.set(cell.slug, said);
    }
    return recordOutput(said, runOcel(cell, dir, phase, name, args, env));
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
