import type { Check, ContractContext } from "./contract";
import type { Cell, LadderCheck, LadderPoint, Leg } from "./matrix/types";
import type { CellContext } from "./targets/types";

export type CellRun = {
  cell: CellContext;
  beforeUp: () => Promise<void>;
  up: () => Promise<void>;
  redeploy: () => Promise<void>;
  rollback: () => Promise<void>;
  destroy: () => Promise<void>;
  afterDestroy: () => Promise<void>;
  live: (app: string, leg: Leg) => ContractContext;
};

export type Step = {
  app: string;
  title: string;
  leg?: Leg;
  run: (cell: CellRun) => Promise<void>;
};

export const UP = "up";
const REDEPLOY = "redeploy";
const ROLLBACK = "rollback";
const DESTROY = "destroy";
const REFUSE = "refuse";

export type CheckLeg = "contract" | "redeploy" | "rollback";

const CHECK_LEGS: CheckLeg[] = ["contract", "redeploy", "rollback"];

function checkTitle(leg: Leg, title: string): string {
  return leg === "contract" ? title : `${leg} · ${title}`;
}

function ladderTitle(at: LadderPoint, title: string): string {
  return `${at} · ${title}`;
}

export type TestRef = { titles: string[] };

export const step = {
  up: { titles: [UP] },
  redeploy: { titles: [REDEPLOY] },
  rollback: { titles: [ROLLBACK] },
  destroy: { titles: [DESTROY] },
  refuse: { titles: [REFUSE] },
} satisfies Record<string, TestRef>;

export function check(checks: Check | Check[], legs: CheckLeg[] = CHECK_LEGS): TestRef {
  const listed = Array.isArray(checks) ? checks : [checks];
  return { titles: listed.flatMap((one) => legs.map((leg) => checkTitle(leg, one.title))) };
}

export function stepsOf(cell: Cell, legs: Leg[]): Step[] {
  const { apps, checks, ladder } = cell.fixture;
  const at = (point: LadderPoint): LadderCheck[] =>
    (ladder?.checks ?? []).filter((one) => one.at === point);
  const perApp = (make: (app: string) => Step[]): Step[] => apps.flatMap(make);
  const has = (leg: Leg) => legs.includes(leg);

  const verified = (app: string, leg: CheckLeg): Step[] => [
    ...checks.map((one) => ({
      app,
      title: checkTitle(leg, one.title),
      leg,
      run: async (run: CellRun) => {
        await run.up();
        await one.run(run.live(app, leg));
      },
    })),
    ...at("consume").map((one) => ({
      app,
      title: checkTitle(leg, ladderTitle("consume", one.title)),
      leg,
      run: async (run: CellRun) => {
        await run.beforeUp();
        await one.run(run.cell, run.live(app, leg));
      },
    })),
  ];

  const replaced = (leg: "redeploy" | "rollback", title: string): Step[] =>
    has(leg)
      ? [
          ...perApp((app) => [{ app, title, leg, run: (run) => run[leg]() }]),
          ...perApp((app) => verified(app, leg)),
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
    ...(has("up")
      ? perApp((app) => [
          {
            app,
            title: UP,
            leg: "up" as const,
            run: async (run: CellRun) => {
              await run.beforeUp();
              await run.up();
            },
          },
        ])
      : []),
    ...(has("contract") ? perApp((app) => verified(app, "contract")) : []),
    ...replaced("redeploy", REDEPLOY),
    ...replaced("rollback", ROLLBACK),
    ...(has("destroy")
      ? perApp((app) => [
          { app, title: DESTROY, leg: "destroy" as const, run: (run: CellRun) => run.destroy() },
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
