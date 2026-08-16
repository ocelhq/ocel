import { createHash } from "node:crypto";
import { checkTarget, runLink, type Target } from "./cli.js";
import { customLink, type DescribedCustom } from "./custom.js";
import type { Grant, SSTInclude } from "./grants.js";
import { postgresLink, type DescribedPostgres } from "./postgres.js";

/** An SST component, as SST already describes itself to its own link consumers. */
export interface SSTPostgresLinkable {
  getSSTLink(): {
    properties: Record<string, unknown>;
    include?: SSTInclude[];
  };
}

type Input<T> = T | Promise<T> | { apply(f: (value: T) => unknown): unknown };

/**
 * A postgres resource described by hand, for anything SST does not describe.
 *
 * The fields are the ones `links.v1.PostgresProperties` declares, and the grants
 * are explicit: nothing is inferred about what an app may do with the resource.
 */
export interface DescribedPostgresResource {
  host: Input<string>;
  port: Input<number>;
  database: Input<string>;
  username: Input<string>;
  password: Input<string>;
  grants?: Grant[];
}

/** Where a link lands: an ocel class, one preview environment, and the project holding both. */
export interface LinkOptions {
  class?: "production" | "preview";
  environment?: string;
  project?: string;
}

interface LinkInputs extends Target {
  name: string;
  owner: string;
}

interface PostgresInputs extends LinkInputs, DescribedPostgres {}

interface CustomInputs extends LinkInputs, DescribedCustom {}

interface LinkState extends Target {
  name: string;
  owner: string;
  digest: string;
}

function linkProvider<I extends LinkInputs>(
  recordFor: (inputs: I) => object,
  resolved: (inputs: I) => boolean,
) {
  const digestOf = (inputs: I) =>
    createHash("sha256")
      .update(JSON.stringify(recordFor(inputs)))
      .digest("hex");

  const stateOf = (inputs: I): LinkState => ({
    name: inputs.name,
    owner: inputs.owner,
    project: inputs.project,
    class: inputs.class,
    environment: inputs.environment,
    digest: digestOf(inputs),
  });

  const set = (inputs: I) =>
    runLink(
      ["set", "--owner", inputs.owner],
      inputs,
      `${JSON.stringify(recordFor(inputs))}\n`,
    );

  return {
    async create(inputs: I) {
      set(inputs);
      return { id: idFor(inputs), outs: stateOf(inputs) };
    },

    async diff(_id: string, olds: LinkState, news: I) {
      const replaces = replacesFor(olds, news);
      return {
        changes:
          replaces.length > 0 ||
          !resolved(news) ||
          olds.digest !== digestOf(news),
        replaces,
        deleteBeforeReplace: replaces.length > 0,
      };
    },

    async update(_id: string, _olds: LinkState, news: I) {
      set(news);
      return { outs: stateOf(news) };
    },

    async delete(_id: string, props: LinkState) {
      runLink(["rm", props.name], props);
    },
  };
}

export const postgresProvider = linkProvider<PostgresInputs>(
  (inputs) =>
    postgresLink(inputs.name, {
      properties: inputs.properties,
      include: inputs.include,
      grants: inputs.grants,
    }),
  (inputs) => linkFields.every((field) => inputs.properties[field] !== undefined),
);

export const customProvider = linkProvider<CustomInputs>(
  (inputs) => customLink(inputs.name, { properties: inputs.properties }),
  (inputs) =>
    Object.values(inputs.properties).every((value) => value !== undefined),
);

/**
 * Publishes one SST-defined resource as one ocel link, as a side effect of this apply.
 *
 * The name is the one the app declares — `postgres("orders")` in ocel,
 * `postgres("orders", …)` here — and the resource is either an SST component,
 * whose own link description is passed through, or the postgres fields written
 * out by hand. `class` defaults to production, `environment` names one preview
 * environment, and `project` is the directory holding `ocel.config.ts`, which is
 * the SST config root unless it is given.
 */
export function postgres(
  name: string,
  resource: SSTPostgresLinkable | DescribedPostgresResource,
  opts?: LinkOptions,
): void {
  const util = host();
  const target: Target = {
    project: opts?.project ?? configRoot(),
    class: opts?.class ?? "production",
    environment: opts?.environment,
  };
  checkTarget(target);

  const logical = `ocel-link-${name}`;
  new util.dynamic.Resource(postgresProvider, logical, {
    ...target,
    name,
    owner: ownerFor(util, logical),
    ...describe(resource),
  });
}

/**
 * A record only transforms read: the properties written out by hand, under a
 * name a transform names.
 *
 * Ocel neither types nor interprets what a custom link carries — it hands the
 * values to a transform that fills a surface field with them, so nothing is
 * delivered to an app and no grants are accepted.
 */
export interface DescribedCustomResource {
  properties: Record<string, Input<unknown>>;
}

/**
 * Publishes one set of values your own infrastructure holds as one ocel custom
 * link, as a side effect of this apply.
 *
 * The name is the one a transform reads — `link.custom("network", …)` here,
 * `links.network.subnetIds` in a transform module. `class` defaults to
 * production, `environment` names one preview environment, and `project` is the
 * directory holding `ocel.config.ts`, which is the SST config root unless it is
 * given.
 */
export function custom(
  name: string,
  resource: DescribedCustomResource,
  opts?: LinkOptions,
): void {
  const util = host();
  const target: Target = {
    project: opts?.project ?? configRoot(),
    class: opts?.class ?? "production",
    environment: opts?.environment,
  };
  checkTarget(target);

  const logical = `ocel-link-${name}`;
  new util.dynamic.Resource(customProvider, logical, {
    ...target,
    name,
    owner: ownerFor(util, logical),
    properties: resource.properties,
  });
}

function idFor(inputs: LinkInputs): string {
  return [inputs.class, inputs.environment, inputs.name]
    .filter(Boolean)
    .join("/");
}

const identity = ["name", "owner", "project", "class", "environment"] as const;

function replacesFor(olds: LinkState, news: LinkInputs): string[] {
  return identity.filter((field) => olds[field] !== news[field]);
}

const linkFields = [
  "host",
  "port",
  "database",
  "username",
  "password",
] as const;

function describe(
  resource: SSTPostgresLinkable | DescribedPostgresResource,
): DescribedPostgres {
  if (typeof (resource as SSTPostgresLinkable).getSSTLink === "function") {
    const described = (resource as SSTPostgresLinkable).getSSTLink();
    return {
      properties: pick(described.properties),
      include: described.include,
    };
  }
  const described = resource as DescribedPostgresResource;
  return {
    properties: pick(described as unknown as Record<string, unknown>),
    grants: described.grants,
  };
}

function pick(properties: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of linkFields) {
    out[field] = properties[field];
  }
  return out;
}

interface DynamicHost {
  getStack(): string;
  getProject(): string;
  dynamic: {
    Resource: new (
      provider: unknown,
      name: string,
      props: Record<string, unknown>,
    ) => unknown;
  };
}

const dynamicType = "pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource";

function ownerFor(util: DynamicHost, logical: string): string {
  return [
    `urn:pulumi:${util.getStack()}`,
    util.getProject(),
    dynamicType,
    logical,
  ].join("::");
}

function host(): DynamicHost {
  const util = (globalThis as { $util?: DynamicHost }).$util;
  if (!util?.dynamic?.Resource) {
    throw new Error(
      "@ocel/sst links from inside sst.config.ts, where SST provides $util; it found no $util here",
    );
  }
  return util;
}

function configRoot(): string {
  const cli = (globalThis as { $cli?: { paths?: { root?: string } } }).$cli;
  const root = cli?.paths?.root;
  if (!root) {
    throw new Error(
      "@ocel/sst publishes into the ocel project holding ocel.config.ts, and SST named no config root here; pass `project` to say which directory holds it",
    );
  }
  return root;
}
