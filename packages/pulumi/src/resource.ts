import { createUrn, dynamic, type Input, type Resource } from "@pulumi/pulumi";
import { createHash } from "node:crypto";
import { checkTarget, runLink, type Target } from "./cli.js";
import { customLink, type DescribedCustom } from "./custom.js";
import type { Grant } from "./grants.js";
import { postgresLink, type DescribedPostgres } from "./postgres.js";

/**
 * A postgres resource described from what a Pulumi resource exposes.
 *
 * The fields are the ones `links.v1.PostgresProperties` declares, each of them
 * an `Input`, so a resource's outputs are handed over as they are and resolved
 * before the record is published. The grants are explicit: nothing is inferred
 * about what an app may do with the resource.
 */
export interface DescribedPostgresResource {
  host: Input<string>;
  port: Input<number>;
  database: Input<string>;
  username: Input<string>;
  password: Input<string>;
  grants?: Grant[];
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
 * Where a link lands: an ocel class, one preview environment, the project
 * holding both, and the Pulumi resource this one hangs under.
 */
export interface LinkOptions {
  class?: "production" | "preview";
  environment?: string;
  project?: string;
  parent?: Resource;
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
      grants: inputs.grants,
    }),
  (inputs) =>
    linkFields.every((field) => inputs.properties[field] !== undefined),
);

export const customProvider = linkProvider<CustomInputs>(
  (inputs) => customLink(inputs.name, { properties: inputs.properties }),
  (inputs) =>
    Object.values(inputs.properties).every((value) => value !== undefined),
);

/**
 * Publishes one Pulumi-defined resource as one ocel link, as a side effect of
 * this update.
 *
 * The name is the one the app declares — `postgres("orders")` in ocel,
 * `postgres("orders", …)` here — and the resource is the postgres fields read
 * off whatever provisioned it, each of them an output this update resolves.
 * `class` defaults to production, `environment` names one preview environment,
 * and `project` is the directory holding `ocel.config.ts`, which is the
 * directory Pulumi runs the program from unless it is given.
 */
export function postgres(
  name: string,
  resource: DescribedPostgresResource,
  opts?: LinkOptions,
): void {
  declare(postgresProvider, name, opts, describe(resource));
}

/**
 * Publishes one set of values your own infrastructure holds as one ocel custom
 * link, as a side effect of this update.
 *
 * The name is the one a transform reads — `link.custom("network", …)` here,
 * `links.network.subnetIds` in a transform module. `class` defaults to
 * production, `environment` names one preview environment, and `project` is the
 * directory holding `ocel.config.ts`, which is the directory Pulumi runs the
 * program from unless it is given.
 */
export function custom(
  name: string,
  resource: DescribedCustomResource,
  opts?: LinkOptions,
): void {
  declare(customProvider, name, opts, { properties: resource.properties });
}

const dynamicType = "pulumi-nodejs:dynamic:Resource";

function declare(
  provider: dynamic.ResourceProvider,
  name: string,
  opts: LinkOptions | undefined,
  described: object,
): void {
  const target: Target = {
    project: opts?.project ?? process.cwd(),
    class: opts?.class ?? "production",
    environment: opts?.environment,
  };
  checkTarget(target);

  const logical = `ocel-link-${name}`;
  new LinkResource(
    provider,
    logical,
    {
      ...target,
      name,
      owner: createUrn(logical, dynamicType, opts?.parent),
      ...described,
    },
    { parent: opts?.parent },
  );
}

class LinkResource extends dynamic.Resource {}

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

function describe(resource: DescribedPostgresResource): DescribedPostgres {
  return {
    properties: pick(resource as unknown as Record<string, unknown>),
    grants: resource.grants,
  };
}

function pick(properties: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of linkFields) {
    out[field] = properties[field];
  }
  return out;
}
