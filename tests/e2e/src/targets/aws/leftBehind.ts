import { readResultData } from "../../ocel";

export const BOOTSTRAP_STATUS_ARGS = ["bootstrap", "status", "production", "--json"];

const STATUS_COMMAND = `ocel ${BOOTSTRAP_STATUS_ARGS.join(" ")}`;

const PRODUCTION = "TIER_PRODUCTION";

type Part = { name: string; retiresWithLastUser: boolean };

type TierStatus = { tier: string; parts: Part[] };

export function readRetiringParts(stdout: string): string[] {
  const { tiers } = readResultData(stdout, STATUS_COMMAND);
  if (!Array.isArray(tiers)) {
    throw new Error(`${STATUS_COMMAND} printed no tiers: ${stdout.trim()}`);
  }
  return tiers
    .filter((one: TierStatus) => one.tier === PRODUCTION)
    .flatMap((one: TierStatus) => one.parts)
    .filter((part: Part) => part.retiresWithLastUser)
    .map((part: Part) => part.name);
}

const listed = new Intl.ListFormat("en", { type: "conjunction" });

export function refuseWhatDestroyLeft(
  slug: string,
  parts: string[],
  trustStoreObjects: string[],
): Error | undefined {
  const left: string[] = [];
  if (parts.length > 0) {
    const retire = parts.length === 1 ? "retires with its" : "retire with their";
    left.push(`${listed.format(parts)}, which ${retire} last user`);
  }
  if (trustStoreObjects.length > 0) {
    left.push(`${listed.format(trustStoreObjects)}, kept for the load balancer's trust store`);
  }
  if (left.length === 0) {
    return undefined;
  }
  return new Error(
    `ocel projects destroy removed ${slug}, its namespace's only project, and left its production tier holding ${left.join(", and ")}`,
  );
}
