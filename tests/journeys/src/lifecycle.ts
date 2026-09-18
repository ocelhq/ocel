import type { Check, CheckContext } from "./checks/context";
import type { Cell, Fixture, LadderCheck, LadderPoint, Phase } from "./matrix/types";
import { type CellContext, hasReleaseCycle, type ReleaseCycle, type Target } from "./targets/types";

export type CellRun = {
  cell: CellContext;
  beforeUp: () => Promise<void>;
  deploy: () => Promise<void>;
  redeploy: () => Promise<void>;
  rollback: () => Promise<void>;
  destroy: () => Promise<void>;
  afterDestroy: () => Promise<void>;
  live: (app: string, phase: Phase) => CheckContext;
};

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
const REFUSE = "refuse";

export type CheckedPhase = "verify" | "redeploy" | "rollback";

const CHECKED_PHASES: CheckedPhase[] = ["verify", "redeploy", "rollback"];

function checkTitle(phase: CheckedPhase, title: string): string {
  return phase === "verify" ? title : `${phase} · ${title}`;
}

function ladderTitle(at: LadderPoint, title: string): string {
  return `${at} · ${title}`;
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
  const { apps, checks, ladder } = cell.fixture;
  const at = (point: LadderPoint): LadderCheck[] =>
    (ladder?.checks ?? []).filter((one) => one.at === point);
  const perApp = (make: (app: string) => Step[]): Step[] => apps.flatMap(make);
  const has = (phase: Phase) => phases.includes(phase);

  const verified = (app: string, phase: CheckedPhase): Step[] => [
    ...checks.map((one) => ({
      app,
      title: checkTitle(phase, one.title),
      phase,
      run: async (run: CellRun) => {
        await run.deploy();
        await one.run(run.live(app, phase));
      },
    })),
    ...at("consume").map((one) => ({
      app,
      title: checkTitle(phase, ladderTitle("consume", one.title)),
      phase,
      run: async (run: CellRun) => {
        await run.beforeUp();
        await one.run(run.cell, run.live(app, phase));
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

  const refuse = ladder?.refuse;
  return [
    ...perApp((app) => [
      ...(refuse ? [{ app, title: REFUSE, run: (run: CellRun) => refuse(run.cell) }] : []),
      ...at("publish").map((one) => ({
        app,
        title: ladderTitle("publish", one.title),
        run: async (run: CellRun) => {
          await run.beforeUp().catch(() => undefined);
          await one.run(run.cell);
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
              await run.beforeUp();
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
      ...at("outlive").map((one) => ({
        app,
        title: ladderTitle("outlive", one.title),
        run: async (run: CellRun) => {
          await run.beforeUp();
          await one.run(run.cell);
        },
      })),
      ...at("prune").map((one) => ({
        app,
        title: ladderTitle("prune", one.title),
        run: async (run: CellRun) => {
          await run.beforeUp();
          await run.afterDestroy();
          await one.run(run.cell);
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
