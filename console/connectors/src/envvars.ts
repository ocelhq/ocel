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
}

export interface Revealed extends Cell {
  value: string;
}

export interface Version {
  version: number;
  createdAt: number;
  size: number;
}

function tierOf(environmentClass: EnvironmentClass): Tier {
  return environmentClass === "preview" ? Tier.PREVIEW : Tier.PRODUCTION;
}

function cellOf(at: { key?: string; folder?: string; environment?: string } | undefined): Cell {
  return { key: at?.key ?? "", folder: at?.folder ?? "", environment: at?.environment ?? "" };
}

export function list(
  connector: Connector,
  environmentClass: EnvironmentClass,
  slug: string,
): Promise<Outcome<Stored[]>> {
  return ask(async () => {
    const answer = await vars(connector).listValues({ tier: tierOf(environmentClass), slug });
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
    }));
  });
}

export function reveal(
  connector: Connector,
  environmentClass: EnvironmentClass,
  slug: string,
  cells: readonly Cell[],
): Promise<Outcome<Revealed[]>> {
  return ask(async () => {
    const answer = await vars(connector).revealValues({
      tier: tierOf(environmentClass),
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
  environmentClass: EnvironmentClass,
  slug: string,
  at: Cell,
  value: string,
  expectedVersion?: number,
): Promise<Outcome<void>> {
  return ask(async () => {
    await vars(connector).setValue({
      tier: tierOf(environmentClass),
      coordinate: { slug, ...at },
      value,
      ...(expectedVersion !== undefined &&
        expectedVersion > 0 && { expectedVersion: BigInt(expectedVersion) }),
    });
  });
}

export function remove(
  connector: Connector,
  environmentClass: EnvironmentClass,
  slug: string,
  at: Cell,
  expectedVersion?: number,
): Promise<Outcome<void>> {
  return ask(async () => {
    await vars(connector).deleteValue({
      tier: tierOf(environmentClass),
      coordinate: { slug, ...at },
      ...(expectedVersion !== undefined &&
        expectedVersion > 0 && { expectedVersion: BigInt(expectedVersion) }),
    });
  });
}

export function versions(
  connector: Connector,
  environmentClass: EnvironmentClass,
  slug: string,
  at: Cell,
): Promise<Outcome<Version[]>> {
  return ask(async () => {
    const answer = await vars(connector).listVersions({
      tier: tierOf(environmentClass),
      coordinate: { slug, ...at },
    });
    return answer.versions.map((entry) => ({
      version: Number(entry.version),
      createdAt: Number(entry.createdAt),
      size: Number(entry.size),
    }));
  });
}
