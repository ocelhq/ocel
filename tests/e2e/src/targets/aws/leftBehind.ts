const STATUS_COMMAND = "ocel bootstrap status production --json";

const PRODUCTION = "TIER_PRODUCTION";

type Part = { name?: string; retiresWithLastUser?: boolean };

type TierStatus = { tier?: string; parts?: Part[] };

function tiersIn(line: string): TierStatus[] | undefined {
  try {
    const tiers = (JSON.parse(line) as { data?: { tiers?: unknown } } | null)?.data?.tiers;
    return Array.isArray(tiers) ? (tiers as TierStatus[]) : undefined;
  } catch {
    return undefined;
  }
}

export function readRetiringParts(stdout: string): string[] {
  const tiers = stdout
    .split("\n")
    .map(tiersIn)
    .filter((found) => found !== undefined)
    .at(-1);
  if (!tiers) {
    throw new Error(`\`${STATUS_COMMAND}\` printed no tiers`);
  }
  return tiers
    .filter((one) => one.tier === PRODUCTION)
    .flatMap((one) => one.parts ?? [])
    .filter((part) => part.retiresWithLastUser === true)
    .map((part) => part.name ?? "");
}

function listed(names: string[]): string {
  return names.length < 2
    ? names.join("")
    : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

export function refuseWhatDestroyLeft(
  slug: string,
  parts: string[],
  trustStoreObjects: string[],
): Error | undefined {
  const left = [
    ...(parts.length === 0
      ? []
      : [
          `${listed(parts)}, which ${parts.length === 1 ? "retires with its" : "retire with their"} last user`,
        ]),
    ...(trustStoreObjects.length === 0
      ? []
      : [`${listed(trustStoreObjects)}, kept for the load balancer's trust store`]),
  ];
  if (left.length === 0) {
    return undefined;
  }
  return new Error(
    `ocel destroy production removed ${slug}, its namespace's only project, and left its production tier holding ${left.join(", and ")}`,
  );
}
