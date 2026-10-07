import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { tmpdir } from "node:os";
import {
  type CheckContext,
  INITIAL_GREETING,
  REDEPLOY_GREETING,
  secretGuarded,
} from "../checks/context";
import {
  type Clock,
  checkOwnReleases,
  checkPruned,
  checkRefusedWithoutIdentity,
  PREVIEW_NAME,
  type PreviewCheck,
} from "../checks/previews";
import type { Evidence } from "../evidence";
import { projectSlug } from "../identity";
import type { Cell, Fixture, Phase, Refusal, Variant } from "../matrix/types";
import { configTree } from "../ocel";
import { fixtureDir } from "../paths";
import type { StackCheck } from "../stacks";
import { namespaceOfSlug } from "../targets/aws/namespace";
import {
  type Deployment,
  hasCommands,
  hasExposure,
  hasPreviews,
  hasReleaseCycle,
  hasRestart,
  type PreviewRelease,
  type Target,
} from "../targets/types";

const LIVE_CLOCK: Clock = {
  fetch: globalThis.fetch,
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  now: () => Date.now(),
};

export function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export type CellUnderTest = Pick<
  CellRun,
  "name" | "fixture" | "variant" | "dir" | "slug" | "runId" | "evidence" | "journeyNonce"
>;

export class CellRun {
  readonly name: string;
  readonly fixture: Fixture;
  readonly variant: Variant;
  readonly dir: string;
  readonly slug: string;
  readonly runId: string;
  readonly evidence: Evidence;
  readonly journeyNonce: string = randomUUID();

  private readonly target: Target;
  private readonly keep: boolean;
  private readonly started = new Map<string, Promise<void>>();
  private readonly notes = new Map<string, string>();
  private unprepared: { error: unknown } | undefined;
  private deployment: Deployment | undefined;
  private greeting = INITIAL_GREETING;
  private previews: { first: PreviewRelease[]; second: PreviewRelease[] } | undefined;
  private readonly clock: Clock;

  constructor(input: {
    cell: Cell;
    target: Target;
    runId: string;
    keep: boolean;
    evidence: Evidence;
    prepareFailure?: string;
    clock?: Clock;
  }) {
    this.name = input.cell.name;
    this.fixture = input.cell.fixture;
    this.variant = input.cell.variant;
    this.dir = fixtureDir(input.cell.fixture.name);
    this.slug = projectSlug(input.cell.name, input.runId);
    this.runId = input.runId;
    this.evidence = input.evidence;
    this.target = input.target;
    this.keep = input.keep;
    this.clock = input.clock ?? LIVE_CLOCK;
    if (input.prepareFailure !== undefined) {
      this.unprepared = { error: new Error(input.prepareFailure) };
    }
  }

  async prepareProcess(): Promise<void> {
    await this.target.prepareProcess().catch((error: unknown) => {
      this.unprepared = { error };
    });
  }

  async refuse(): Promise<void> {
    this.ready();
    await this.fixture.stack?.refuse(this);
  }

  async checkStack(check: StackCheck, serving?: CheckContext): Promise<void> {
    this.ready();
    await check.run(this, serving);
  }

  deployStack(): Promise<void> {
    return this.once("deployStack", async () => {
      this.ready();
      await this.fixture.stack?.deploy(this);
    });
  }

  deploy(): Promise<void> {
    return this.once("deploy", async () => {
      this.ready();
      this.deployment = await this.target.deploy(this);
    });
  }

  async refusedDeploy(refusal: Refusal): Promise<void> {
    this.ready();
    const failure = await this.target.deploy(this).then(
      () => undefined,
      (error: unknown) => ({ error }),
    );
    assert.ok(failure, `${this.target.name} deployed ${this.name}, whose build is refused`);
    await refusal.run(messageOf(failure.error));
  }

  async restart(): Promise<void> {
    await this.deploy();
    await this.once("restart", async () => {
      const target = this.target;
      assert.ok(hasRestart(target), `${target.name} has nothing to restart what an app declared`);
      this.deployment = await target.restart(this);
    });
  }

  async redeploy(): Promise<void> {
    await this.deploy();
    await this.once("redeploy", async () => {
      const target = this.releaseCycle("redeploy");
      this.deployment = await target.redeploy(this, REDEPLOY_GREETING);
      this.greeting = REDEPLOY_GREETING;
    });
  }

  async rollback(): Promise<void> {
    await this.deploy();
    await this.once("rollback", async () => {
      const target = this.releaseCycle("roll back");
      this.deployment = await target.rollback(this, INITIAL_GREETING);
      this.greeting = INITIAL_GREETING;
    });
  }

  previewUp(): Promise<void> {
    return this.once("previewUp", async () => {
      this.ready();
      const target = this.previewing();
      const first = await target.previewUp(this, PREVIEW_NAME);
      const second = await target.previewUp(this, PREVIEW_NAME);
      for (const app of this.fixture.apps) {
        for (const released of [first, second]) {
          if (!released.some((each) => each.app === app)) {
            throw new Error(`the preview of ${this.name} released nothing for ${app}`);
          }
        }
      }
      this.previews = { first, second };
      await this.evidence.write(
        "preview",
        "releases.json",
        `${JSON.stringify({ first, second }, null, 2)}\n`,
      );
    });
  }

  async preview(app: string, check: PreviewCheck): Promise<void> {
    await this.previewUp();
    const first = this.previewOf(app, "first");
    const second = this.previewOf(app, "second");
    switch (check) {
      case "own-release":
        await checkOwnReleases(this.clock, app, first, second);
        return;
      case "pruned": {
        const target = this.previewing();
        await this.once("previewPrune", () => target.previewPrune(this, PREVIEW_NAME, 1));
        const seen = await checkPruned(this.clock, app, first, second);
        await this.evidence.write("preview", `pruned-${app}.json`, `${JSON.stringify({ seen })}\n`);
        return;
      }
      case "refused": {
        const seen = await checkRefusedWithoutIdentity(this.clock, [first, second]);
        await this.evidence.write(
          "preview",
          `refused-${app}.json`,
          `${JSON.stringify(seen, null, 2)}\n`,
        );
        return;
      }
    }
  }

  async destroy(): Promise<void> {
    await this.tearDown();
    assert.ok(
      !(await this.target.sweeper.exists(this.slug)),
      `${this.slug} still exists on ${this.target.name} after destroy`,
    );
  }

  destroyStack(): Promise<void> {
    return this.once("destroyStack", async () => {
      if (!this.keep) {
        await this.fixture.stack?.destroy(this);
      }
    });
  }

  async verify(
    app: string,
    phase: Phase,
    work: (ctx: CheckContext) => Promise<void>,
  ): Promise<void> {
    assert.ok(this.deployment, "a check ran before the cell was deployed");
    const guard = secretGuarded(this.deployment.fetch);
    const failure = await work({
      app,
      baseUrl: this.deployment.baseUrl(app),
      greeting: this.greeting,
      maxRequestBodyBytes: this.target.maxRequestBodyBytes,
      phase,
      notes: this.notes,
      fetch: guard.fetch,
      reach: this.deployment.reach ?? (async (url) => url),
      readExposed: () => this.readExposed(),
      runInEnvironment: (command) => this.runInEnvironment(command),
      journeyNonce: this.journeyNonce,
      projectDir: configTree(this, this.target.name),
      tempDir: tmpdir(),
    }).then(
      () => undefined,
      (error: unknown) => ({ error }),
    );
    try {
      await guard.settle();
    } catch (leak) {
      if (failure) {
        throw new Error(`${messageOf(leak)}; the check also failed: ${messageOf(failure.error)}`, {
          cause: failure.error,
        });
      }
      throw leak;
    }
    if (failure) {
      throw failure.error;
    }
  }

  private async runInEnvironment(command: string[]): Promise<string> {
    const target = this.target;
    assert.ok(
      hasCommands(target),
      `${target.name} has no way to run a command against a deployed environment`,
    );
    return target.runInEnvironment(this, command);
  }

  private async readExposed(): Promise<string> {
    const target = this.target;
    assert.ok(
      hasExposure(target),
      `${target.name} has no way to read what it exposes of a cell outside its apps`,
    );
    return target.readExposed(this);
  }

  async finish(phases: Phase[]): Promise<void> {
    if (!this.keep) {
      await this.tearDown().catch(() => undefined);
      return;
    }
    const [last] = phases.slice(-1);
    if (last) {
      await this.evidence
        .write(
          last,
          "kept.json",
          `${JSON.stringify({ slug: this.slug, namespace: namespaceOfSlug(this.slug) }, null, 2)}\n`,
        )
        .catch(() => undefined);
    }
  }

  private tearDown(): Promise<void> {
    return this.once("tearDown", async () => {
      const target = this.target;
      const removed =
        this.started.has("previewUp") && hasPreviews(target)
          ? await target.previewRemove(this, PREVIEW_NAME).then(
              () => undefined,
              (error: unknown) => ({ error }),
            )
          : undefined;
      const destroyed = await target.destroy(this).then(
        () => undefined,
        (error: unknown) => ({ error }),
      );
      if (removed || destroyed) {
        throw new Error(
          [
            ...(removed ? [`removing preview ${PREVIEW_NAME}: ${messageOf(removed.error)}`] : []),
            ...(destroyed ? [`destroying: ${messageOf(destroyed.error)}`] : []),
          ].join("; "),
          { cause: (removed ?? destroyed)?.error },
        );
      }
    });
  }

  private previewing() {
    const target = this.target;
    assert.ok(hasPreviews(target), `${target.name} has no previews to walk`);
    return target;
  }

  private previewOf(app: string, which: "first" | "second"): PreviewRelease {
    const found = this.previews?.[which].find((each) => each.app === app);
    assert.ok(found, `the preview of ${this.name} released nothing for ${app}`);
    return found;
  }

  private ready(): void {
    if (this.unprepared) {
      throw this.unprepared.error;
    }
  }

  private releaseCycle(doing: string) {
    const target = this.target;
    assert.ok(hasReleaseCycle(target), `${target.name} has no release cycle to ${doing} with`);
    return target;
  }

  private once(step: string, work: () => Promise<void>): Promise<void> {
    let started = this.started.get(step);
    if (!started) {
      started = work();
      this.started.set(step, started);
    }
    return started;
  }
}
