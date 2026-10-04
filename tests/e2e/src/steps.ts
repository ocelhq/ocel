import type { Check } from "./checks/context";
import type { Cell, Fixture, Phase } from "./matrix/types";
import type { CellRun } from "./run/cellRun";
import type { StackPoint } from "./stacks";
import { hasReleaseCycle, type ReleaseCycle, type Target } from "./targets/types";

export type Step = {
  app: string;
  title: string;
  phase?: Phase;
  run: (run: CellRun) => Promise<void>;
};

export const DEPLOY = "deploy";
const RESTART = "restart";
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

export type CheckedPhase = "verify" | "restart" | "redeploy" | "rollback";

const CHECKED_PHASES: CheckedPhase[] = ["verify", "restart", "redeploy", "rollback"];

function checkTitle(phase: CheckedPhase, title: string): string {
  return phase === "verify" ? title : `${phase} · ${title}`;
}

function stackCheckTitle(point: StackPoint, title: string): string {
  return `${STACK_POINT_TITLES[point]} · ${title}`;
}

declare const built: unique symbol;

export type TestSelector = { readonly titles: string[]; readonly [built]: true };

function selector(titles: string[]): TestSelector {
  return { titles } as TestSelector;
}

export const step = {
  deploy: selector([DEPLOY]),
  redeploy: selector([REDEPLOY]),
  rollback: selector([ROLLBACK]),
  destroy: selector([DESTROY]),
  refuse: selector([REFUSE]),
};

export function check(
  checks: Check | Check[],
  phases: CheckedPhase[] = CHECKED_PHASES,
): TestSelector {
  const listed = Array.isArray(checks) ? checks : [checks];
  return selector(listed.flatMap((one) => phases.map((phase) => checkTitle(phase, one.title))));
}

export function phasesOf(fixture: Fixture, keep: boolean, releaseCycle = true): Phase[] {
  if (fixture.refusal) {
    return ["deploy", ...(keep ? [] : (["destroy"] as const))];
  }
  const redeploys = fixture.redeploys === true || (fixture.redeploys !== undefined && releaseCycle);
  return [
    "deploy",
    "verify",
    ...(fixture.restarts ? (["restart"] as const) : []),
    ...(redeploys ? (["redeploy", "rollback"] as const) : []),
    ...(keep ? [] : (["destroy"] as const)),
  ];
}

function newDestroyStep(app: string): Step {
  return { app, title: DESTROY, phase: "destroy", run: (run) => run.destroy() };
}

export function stepsOf(cell: Cell, phases: Phase[]): Step[] {
  const { apps, stack, refusal } = cell.fixture;
  if (refusal) {
    return apps.flatMap((app) => [
      ...(phases.includes("deploy")
        ? [
            {
              app,
              title: refusal.title,
              phase: "deploy" as const,
              run: (run: CellRun) => run.refusedDeploy(refusal),
            },
          ]
        : []),
      ...(phases.includes("destroy") ? [newDestroyStep(app)] : []),
    ]);
  }
  const checks = [...cell.fixture.checks, ...(cell.variant.checks ?? [])].filter(
    (one) => one.cacheLayer === undefined || one.cacheLayer === cell.cacheLayer,
  );
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
        await run.verify(app, phase, (ctx) => one.run(ctx));
      },
    })),
    ...at("whileServing").map((one) => ({
      app,
      title: checkTitle(phase, stackCheckTitle("whileServing", one.title)),
      phase,
      run: async (run: CellRun) => {
        await run.deployStack();
        await run.verify(app, phase, (ctx) => run.checkStack(one, ctx));
      },
    })),
  ];

  const replaced = (phase: "restart" | "redeploy" | "rollback", title: string): Step[] =>
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
          await run.checkStack(one);
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
    ...replaced("restart", RESTART),
    ...replaced("redeploy", REDEPLOY),
    ...replaced("rollback", ROLLBACK),
    ...(has("destroy") ? perApp((app) => [newDestroyStep(app)]) : []),
    ...perApp((app) => [
      ...at("afterOcelDestroy").map((one) => ({
        app,
        title: stackCheckTitle("afterOcelDestroy", one.title),
        run: async (run: CellRun) => {
          await run.deployStack();
          await run.checkStack(one);
        },
      })),
      ...at("afterStackDestroy").map((one) => ({
        app,
        title: stackCheckTitle("afterStackDestroy", one.title),
        run: async (run: CellRun) => {
          await run.deployStack();
          await run.destroyStack();
          await run.checkStack(one);
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
