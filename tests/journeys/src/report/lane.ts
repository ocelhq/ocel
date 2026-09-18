import { fixtures } from "../matrix/fixtures";
import { gaps } from "../matrix/gaps";
import { laneNamed, targetOfLane } from "../matrix/types";
import { type Ask, cellKey, type Listed, type Plan, plan } from "../plan";
import { askFrom } from "../run/ask";
import { targetNamed } from "../targets";

const USAGE = "pnpm --filter @ocel-tests/journeys plan --lane <lane>";

function said(listed: Listed[]): string {
  return listed
    .map((gap) => (gap.issue === undefined ? gap.id : `${gap.id} #${gap.issue}`))
    .join(", ");
}

export function laneTable(planned: Plan, ask: Ask): string {
  const lines = [
    `lane ${planned.lane} · target ${planned.target} · coverage ${ask.coverage} · ${planned.cells.length} cells`,
  ];
  for (const [cell, listed] of Object.entries(planned.skipped)) {
    lines.push(`skipped ${cell} — ${said(listed)}`);
  }
  for (const cell of planned.cells) {
    lines.push(
      "",
      `${cell.name} · fixture ${cell.fixture} · variant ${cell.variant} · legs ${cell.legs.join(" ")}`,
    );
    for (const step of cell.steps) {
      const listed = planned.expectations[cellKey(cell.name, step.app)]?.[step.title];
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
  const ask = askFrom(process.env);
  const legs = targetNamed(targetOfLane(lane)).legs;
  process.stdout.write(laneTable(plan({ fixtures, gaps, lane, legs, ask }), ask));
}

try {
  main(process.argv.slice(2));
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
  process.exitCode = 1;
}
