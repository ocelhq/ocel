export const TIMED_LEGS = ["up", "contract", "redeploy", "rollback", "destroy"] as const;

export const OTHER_LEG = "other";

export type TimelineTest = {
  cell: string;
  leg?: string;
  title: string;
  startTime: number;
  duration: number;
};

export type TimelineModule = { cell: string; duration: number };

export type TimelineInput = {
  runStart: number;
  runEnd: number;
  workers: number;
  tests: TimelineTest[];
  modules: TimelineModule[];
  prepareMs?: number;
};

export type CellTiming = {
  cell: string;
  start: number;
  legs: Record<string, number>;
  file: number;
};

export type Tail = { cell: string; seconds: number };

export type Timeline = {
  workers: number;
  wall: number;
  files: number;
  speedUp: number;
  maxOverlap: number;
  tail?: Tail;
  prepare?: number;
  cells: CellTiming[];
};

export type Segment = { from: number; to: number; active: string[] };

function seconds(ms: number): number {
  return Math.round(ms / 1000);
}

export function legOf(test: TimelineTest): string {
  if (test.leg) {
    return test.leg;
  }
  const prefix = test.title.split(" · ")[0];
  return (TIMED_LEGS as readonly string[]).includes(prefix) ? prefix : OTHER_LEG;
}

export function sweep(tests: TimelineTest[]): Segment[] {
  const points = tests
    .filter((test) => test.duration > 0)
    .flatMap((test) => [
      { at: test.startTime, cell: test.cell, delta: 1 },
      { at: test.startTime + test.duration, cell: test.cell, delta: -1 },
    ])
    .sort((a, b) => a.at - b.at || a.delta - b.delta);

  const held = new Map<string, number>();
  const out: Segment[] = [];
  let index = 0;
  let previous = points[0]?.at ?? 0;
  while (index < points.length) {
    const at = points[index].at;
    if (at > previous) {
      const active = [...held.entries()].filter(([, count]) => count > 0).map(([name]) => name);
      if (active.length > 0) {
        out.push({ from: previous, to: at, active });
      }
      previous = at;
    }
    while (index < points.length && points[index].at === at) {
      const point = points[index];
      held.set(point.cell, (held.get(point.cell) ?? 0) + point.delta);
      index += 1;
    }
  }
  return out;
}

function tailOf(segments: Segment[]): Tail | undefined {
  const last = segments.at(-1);
  if (!last || last.active.length !== 1) {
    return undefined;
  }
  const cell = last.active[0];
  let from = last.from;
  for (let index = segments.length - 2; index >= 0; index -= 1) {
    const segment = segments[index];
    if (segment.active.length !== 1 || segment.active[0] !== cell || segment.to !== from) {
      break;
    }
    from = segment.from;
  }
  return { cell, seconds: seconds(last.to - from) };
}

function timingFor(
  cell: string,
  tests: TimelineTest[],
  runStart: number,
  file: number,
): CellTiming {
  const held: Record<string, number> = {};
  for (const test of tests) {
    const leg = legOf(test);
    held[leg] = (held[leg] ?? 0) + test.duration;
  }
  const legs = Object.fromEntries(
    Object.entries(held).map(([leg, ms]) => [leg, seconds(ms)]),
  );
  const first = Math.min(...tests.map((test) => test.startTime));
  return { cell, start: seconds(first - runStart), legs, file: seconds(file) };
}

export function timelineOf(input: TimelineInput): Timeline {
  const wallMs = Math.max(0, input.runEnd - input.runStart);
  const filesMs = input.modules.reduce((total, module) => total + module.duration, 0);
  const byCell = new Map<string, TimelineTest[]>();
  for (const test of input.tests) {
    byCell.set(test.cell, [...(byCell.get(test.cell) ?? []), test]);
  }
  const moduleMs = new Map(input.modules.map((module) => [module.cell, module.duration]));
  const cells = [...byCell.entries()]
    .map(([cell, tests]) => timingFor(cell, tests, input.runStart, moduleMs.get(cell) ?? 0))
    .sort((a, b) => b.file - a.file || a.cell.localeCompare(b.cell));
  const segments = sweep(input.tests);
  return {
    workers: input.workers,
    wall: seconds(wallMs),
    files: seconds(filesMs),
    speedUp: wallMs === 0 ? 0 : Math.round((filesMs / wallMs) * 10) / 10,
    maxOverlap: segments.reduce((most, segment) => Math.max(most, segment.active.length), 0),
    tail: tailOf(segments),
    prepare: input.prepareMs === undefined ? undefined : seconds(input.prepareMs),
    cells,
  };
}

export type TimingMeta = { target: string; runId: string };

export function timingTable(timeline: Timeline, meta: TimingMeta): string {
  const stat = [
    `wall ${timeline.wall}s`,
    `Σ files ${timeline.files}s`,
    `${timeline.workers} workers`,
    `speed-up ${timeline.speedUp.toFixed(1)}x`,
    `max overlap ${timeline.maxOverlap}`,
    ...(timeline.prepare === undefined ? [] : [`prepare ${timeline.prepare}s`]),
    timeline.tail
      ? `tail: ${timeline.tail.cell} alone for ${timeline.tail.seconds}s`
      : "tail: none",
  ].join(" · ");

  const columns = [...TIMED_LEGS, OTHER_LEG];
  return [
    `### timing · ${meta.target} · run ${meta.runId}`,
    "",
    stat,
    "",
    `| cell | start | ${columns.join(" | ")} | file |`,
    `| --- | --- |${" --- |".repeat(columns.length)} --- |`,
    ...timeline.cells.map(
      (row) =>
        `| ${row.cell} | ${row.start} | ${columns.map((leg) => row.legs[leg] ?? 0).join(" | ")} | ${row.file} |`,
    ),
    "",
  ].join("\n");
}
