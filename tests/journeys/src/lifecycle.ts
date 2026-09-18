import type { Check } from "./checks/context";
import type { Cell, Fixture, Phase } from "./matrix/types";
import type { CellRun } from "./run/cellRun";
import type { StackPoint } from "./targets/aws/stacks/bindings";
import { hasReleaseCycle, type ReleaseCycle, type Target } from "./targets/types";

export type Step = {
  app: string;
  title: string;
  phase?: Phase;
  run: (cell: CellRun) => Promise<void>;
};

export const DEPLOY = "deploy";
const REDEPLOY = "redeploy";
const ROLLBACK = "rollback";
const DESTROY = "destroy";
const REFUSE = "ocel refuses before the stack publishes";

const STACK_POINT_TITLES: Record<StackPoint, string> = {
  afterPublish: "after publish",
  whileServing: "while serving",
  afterOcelDestroy: "after ocel destroy",
  afterStackDestroy: "after stack destroy",
};

export type CheckedPhase = "verify" | "redeploy" | "rollback";

const CHECKED_PHASES: CheckedPhase[] = ["verify", "redeploy", "rollback"];

function checkTitle(phase: CheckedPhase, title: string): string {
  return phase === "verify" ? title : `${phase} · ${title}`;
}

function stackCheckTitle(point: StackPoint, title: string): string {
  return `${STACK_POINT_TITLES[point]} · ${title}`;
}

declare const built: unique symbol;

export type TestRef = { readonly titles: string[]; readonly [built]: true };

function testRef(titles: string[]): TestRef {
  return { titles } as TestRef;
}

export const stepRef = {
  deploy: testRef([DEPLOY]),
  redeploy: testRef([REDEPLOY]),
  rollback: testRef([ROLLBACK]),
  destroy: testRef([DESTROY]),
  refuse: testRef([REFUSE]),
};

export function check(checks: Check | Check[], phases: CheckedPhase[] = CHECKED_PHASES): TestRef {
  const listed = Array.isArray(checks) ? checks : [checks];
  return testRef(listed.flatMap((one) => phases.map((phase) => checkTitle(phase, one.title))));
}

export function phasesOf(fixture: Fixture, keep: boolean): Phase[] {
  return [
    "deploy",
    "verify",
    ...(fixture.redeploys ? (["redeploy", "rollback"] as const) : []),
    ...(keep ? [] : (["destroy"] as const)),
  ];
}

export function stepsOf(cell: Cell, phases: Phase[]): Step[] {
  const { apps, checks, stack } = cell.fixture;
  const at = (point: StackPoint) => stack?.checks[point] ?? [];
  const perApp = (make: (app: string) => Step[]): Step[] => apps.flatMap(make);
  const has = (phase: Phase) => phases.includes(phase);

  const verified = (app: string, phase: CheckedPhase): Step[] => [
    ...checks.map((one) => ({
      app,
      title: checkTitle(phase, one.title),
      phase,
      run: async (run: CellRun) => {
        await run.deploy();
        await one.run(run.verifying(app, phase));
      },
    })),
    ...at("whileServing").map((one) => ({
      app,
      title: checkTitle(phase, stackCheckTitle("whileServing", one.title)),
      phase,
      run: async (run: CellRun) => {
        await run.deployStack();
        await one.run(run, run.verifying(app, phase));
      },
    })),
  ];

  const replaced = (phase: "redeploy" | "rollback", title: string): Step[] =>
    has(phase)
      ? [
          ...perApp((app) => [{ app, title, phase, run: (run) => run[phase]() }]),
          ...perApp((app) => verified(app, phase)),
        ]
      : [];

  return [
    ...perApp((app) => [
      ...(stack ? [{ app, title: REFUSE, run: (run: CellRun) => run.refuse() }] : []),
      ...at("afterPublish").map((one) => ({
        app,
        title: stackCheckTitle("afterPublish", one.title),
        run: async (run: CellRun) => {
          await run.deployStack().catch(() => undefined);
          await one.run(run);
        },
      })),
    ]),
    ...(has("deploy")
      ? perApp((app) => [
          {
            app,
            title: DEPLOY,
            phase: "deploy" as const,
            run: async (run: CellRun) => {
              await run.deployStack();
              await run.deploy();
            },
          },
        ])
      : []),
    ...(has("verify") ? perApp((app) => verified(app, "verify")) : []),
    ...replaced("redeploy", REDEPLOY),
    ...replaced("rollback", ROLLBACK),
    ...(has("destroy")
      ? perApp((app) => [
          { app, title: DESTROY, phase: "destroy" as const, run: (run: CellRun) => run.destroy() },
        ])
      : []),
    ...perApp((app) => [
      ...at("afterOcelDestroy").map((one) => ({
        app,
        title: stackCheckTitle("afterOcelDestroy", one.title),
        run: async (run: CellRun) => {
          await run.deployStack();
          await one.run(run);
        },
      })),
      ...at("afterStackDestroy").map((one) => ({
        app,
        title: stackCheckTitle("afterStackDestroy", one.title),
        run: async (run: CellRun) => {
          await run.deployStack();
          await run.destroyStack();
          await one.run(run);
        },
      })),
    ]),
  ];
}

type PlannedSteps = { phases: Phase[]; steps: Array<Pick<Step, "app" | "title" | "phase">> };

function said(steps: PlannedSteps["steps"]): string {
  return steps.map((one) => `${one.app} · ${one.title}`).join(", ");
}

export function stepsPlanned(cell: Cell, planned: PlannedSteps): Step[] {
  const steps = stepsOf(cell, planned.phases);
  const same =
    steps.length === planned.steps.length &&
    steps.every((one, index) => {
      const at = planned.steps[index];
      return at?.app === one.app && at.title === one.title && at.phase === one.phase;
    });
  if (!same) {
    throw new Error(`${cell.name} walks ${said(steps)}, not the planned ${said(planned.steps)}`);
  }
  return steps;
}

export function phasesDriven(
  target: Pick<Target, "name"> & Partial<ReleaseCycle>,
  phases: Phase[],
): Phase[] {
  const cycled = phases.filter((phase) => phase === "redeploy" || phase === "rollback");
  if (cycled.length > 0 && !hasReleaseCycle(target)) {
    throw new Error(
      `${target.name} walks ${cycled.join(", ")} with no release cycle to drive them`,
    );
  }
  return phases;
}
