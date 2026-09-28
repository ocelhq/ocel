const KEEP = "ACTION_KEEP";

const WRITING_ACTIONS = new Set([
  "ACTION_CREATE",
  "ACTION_UPDATE",
  "ACTION_REPLACE",
  "ACTION_DELETE",
  "ACTION_DISABLE_THEN_DELETE",
]);

type PlannedChange = { name?: string; action?: string };

type PlannedGroup = { name?: string; action?: string; changes?: PlannedChange[] };

type RunEvent = { phase?: string; plan?: { groups?: PlannedGroup[] } };

function eventOf(line: string): RunEvent | undefined {
  try {
    const parsed: unknown = JSON.parse(line);
    return typeof parsed === "object" && parsed !== null ? (parsed as RunEvent) : undefined;
  } catch {
    return undefined;
  }
}

export function plannedWrites(stream: string): string[] {
  let planned = false;
  const writes: string[] = [];
  for (const line of stream.split("\n")) {
    const event = eventOf(line);
    if (!event) {
      continue;
    }
    planned ||= event.phase === "PHASE_PLAN";
    for (const group of event.plan?.groups ?? []) {
      const changes = group.changes ?? [];
      for (const change of changes) {
        if (WRITING_ACTIONS.has(change.action ?? "")) {
          writes.push(`${group.name}/${change.name} ${change.action}`);
        }
      }
      const acting = changes.some((change) => change.action !== KEEP);
      if (!acting && WRITING_ACTIONS.has(group.action ?? "")) {
        writes.push(`${group.name} ${group.action}`);
      }
    }
  }
  if (!planned) {
    throw new Error(
      `the run streamed no plan phase, so nothing here reads what it would write:\n${stream}`,
    );
  }
  return writes;
}
