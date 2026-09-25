import { Tier } from "./gen/common/environment/v1/environment_pb";
import type { Outcome } from "./item";
import { ask, type Connector, vars } from "./transport";

export type EnvironmentClass = "production" | "preview";

export interface Cell {
  key: string;
  folder: string;
  environment: string;
}

export interface Stored extends Cell {
  version: number;
  updatedAt: number;
  size: number;
  reference?: { slug: string; folder: string; key: string };
  envSource?: string;
}

export interface EnvSourceStatus {
  id: string;
  writable: boolean;
  links: Record<string, string>;
  credentials: string[];
}

export interface Held extends Cell {
  value: string;
}

export interface Version {
  version: number;
  createdAt: number;
  size: number;
}

function tierOf(held: EnvironmentClass): Tier {
  return held === "preview" ? Tier.PREVIEW : Tier.PRODUCTION;
}

function cellOf(at: { key?: string; folder?: string; environment?: string } | undefined): Cell {
  return { key: at?.key ?? "", folder: at?.folder ?? "", environment: at?.environment ?? "" };
}

export function list(
  connector: Connector,
  held: EnvironmentClass,
  slug: string,
): Promise<Outcome<Stored[]>> {
  return ask(async () => {
    const answer = await vars(connector).listValues({ tier: tierOf(held), slug });
    return answer.values.map((value) => ({
      ...cellOf(value.coordinate),
      version: Number(value.version),
      updatedAt: Number(value.updatedAt),
      size: Number(value.size),
      ...(value.target && {
        reference: {
          slug: value.target.slug,
          folder: value.target.folder,
          key: value.target.key,
        },
      }),
      ...(value.envSource && { envSource: value.envSource }),
    }));
  });
}

export function describeEnvSource(
  connector: Connector,
  held: EnvironmentClass,
  slug: string,
): Promise<Outcome<EnvSourceStatus>> {
  return ask(async () => {
    const answer = await vars(connector).describeEnvSource({ tier: tierOf(held), slug });
    const status = answer.status;
    return {
      id: status?.envSource || "builtin",
      writable: status?.writable ?? false,
      links: Object.fromEntries((status?.links ?? []).map((link) => [link.folder, link.link])),
      credentials: status?.credentials ?? [],
    };
  });
}

export function createInEnvSource(
  connector: Connector,
  held: EnvironmentClass,
  slug: string,
  at: Cell,
  value: string,
  description: string,
): Promise<Outcome<{ awaitingApproval: boolean }>> {
  return ask(async () => {
    const answer = await vars(connector).putEnvSourceValue({
      tier: tierOf(held),
      coordinate: { slug, ...at },
      value,
      description,
    });
    return { awaitingApproval: answer.awaitingApproval };
  });
}

export function reveal(
  connector: Connector,
  held: EnvironmentClass,
  slug: string,
  cells: readonly Cell[],
): Promise<Outcome<Held[]>> {
  return ask(async () => {
    const answer = await vars(connector).revealValues({
      tier: tierOf(held),
      slug,
      cells: cells.map((at) => ({ slug, ...at })),
    });
    return answer.values.map((value) => ({
      ...cellOf(value.metadata?.coordinate),
      value: value.value,
    }));
  });
}

export function set(
  connector: Connector,
  held: EnvironmentClass,
  slug: string,
  at: Cell,
  value: string,
  expectedVersion?: number,
): Promise<Outcome<void>> {
  return ask(async () => {
    await vars(connector).setValue({
      tier: tierOf(held),
      coordinate: { slug, ...at },
      value,
      ...(expectedVersion !== undefined &&
        expectedVersion > 0 && { expectedVersion: BigInt(expectedVersion) }),
    });
  });
}

export function remove(
  connector: Connector,
  held: EnvironmentClass,
  slug: string,
  at: Cell,
  expectedVersion?: number,
): Promise<Outcome<void>> {
  return ask(async () => {
    await vars(connector).deleteValue({
      tier: tierOf(held),
      coordinate: { slug, ...at },
      ...(expectedVersion !== undefined &&
        expectedVersion > 0 && { expectedVersion: BigInt(expectedVersion) }),
    });
  });
}

export function versions(
  connector: Connector,
  held: EnvironmentClass,
  slug: string,
  at: Cell,
): Promise<Outcome<Version[]>> {
  return ask(async () => {
    const answer = await vars(connector).listVersions({
      tier: tierOf(held),
      coordinate: { slug, ...at },
    });
    return answer.versions.map((entry) => ({
      version: Number(entry.version),
      createdAt: Number(entry.createdAt),
      size: Number(entry.size),
    }));
  });
}
