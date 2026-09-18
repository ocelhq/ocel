import assert from "node:assert/strict";
import {
  type CheckContext,
  INITIAL_GREETING,
  REDEPLOY_GREETING,
  secretGuarded,
} from "../checks/context";
import type { Evidence } from "../evidence";
import { projectSlug } from "../identity";
import type { Cell, Fixture, Phase, Variant } from "../matrix/types";
import { fixtureDir } from "../paths";
import type { StackCheck } from "../stacks";
import { namespaceOfSlug } from "../targets/aws/namespace";
import { type Deployment, hasReleaseCycle, type Target } from "../targets/types";

export type CellUnderTest = Pick<
  CellRun,
  "name" | "fixture" | "variant" | "dir" | "slug" | "runId" | "evidence"
>;

export class CellRun {
  readonly name: string;
  readonly fixture: Fixture;
  readonly variant: Variant;
  readonly dir: string;
  readonly slug: string;
  readonly runId: string;
  readonly evidence: Evidence;

  private readonly target: Target;
  private readonly keep: boolean;
  private readonly started = new Map<string, Promise<void>>();
  private readonly notes = new Map<string, string>();
  private unprepared: { error: unknown } | undefined;
  private deployment: Deployment | undefined;
  private greeting = INITIAL_GREETING;

  constructor(input: {
    cell: Cell;
    target: Target;
    runId: string;
    keep: boolean;
    evidence: Evidence;
    prepareFailure?: string;
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

  verifying(app: string, phase: Phase): CheckContext {
    assert.ok(this.deployment, "a check ran before the cell was deployed");
    return {
      app,
      baseUrl: this.deployment.baseUrl(app),
      greeting: this.greeting,
      maxRequestBodyBytes: this.target.maxRequestBodyBytes,
      phase,
      notes: this.notes,
      fetch: secretGuarded(this.deployment.fetch),
    };
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
    return this.once("tearDown", () => this.target.destroy(this));
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
