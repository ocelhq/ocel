import { fixtures } from "../matrix/fixtures";
import { gaps } from "../matrix/gaps";
import { laneNamed, targetOfLane } from "../matrix/types";
import { cellApp, type GapRef, type Plan, plan, type RunFilter } from "../plan";
import { filterFrom } from "../run/filter";
import { hasReleaseCycle, targetNamed } from "../targets";

const USAGE = "pnpm --filter @ocel-tests/journeys plan --lane <lane>";

function said(listed: GapRef[]): string {
  return listed
    .map((gap) => (gap.issue === undefined ? gap.id : `${gap.id} #${gap.issue}`))
    .join(", ");
}

export function laneTable(planned: Plan, filter: RunFilter): string {
  const lines = [
    `lane ${planned.lane} · target ${planned.target} · coverage ${filter.coverage} · ${planned.cells.length} cells`,
  ];
  for (const [cell, listed] of Object.entries(planned.skipped)) {
    lines.push(`skipped ${cell} — ${said(listed)}`);
  }
  for (const cell of planned.cells) {
    lines.push(
      "",
      `${cell.name} · fixture ${cell.fixture} · variant ${cell.variant} · phases ${cell.phases.join(" ")}`,
    );
    for (const step of cell.steps) {
      const listed = planned.expectedFailures[cellApp(cell.name, step.app)]?.[step.title];
      const red = listed ? `  [red: ${said(listed)}]` : "";
      lines.push(`  ${step.app} · ${step.title}${red}`);
    }
  }
  return `${lines.join("\n")}\n`;
}

function main(argv: string[]) {
  const at = argv.indexOf("--lane");
  const named = at === -1 ? undefined : argv[at + 1];
  if (!named) {
    throw new Error(USAGE);
  }
  const lane = laneNamed(named);
  const filter = filterFrom(process.env);
  const releaseCycle = hasReleaseCycle(targetNamed(targetOfLane(lane)));
  process.stdout.write(
    laneTable(plan({ fixtures, gaps, lane, releaseCycle, filter, env: process.env }), filter),
  );
}

try {
  main(process.argv.slice(2));
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
  process.exitCode = 1;
}
