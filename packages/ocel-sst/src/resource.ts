import crypto from "node:crypto";
import { checkTarget, runBindings, type Target } from "./cli.js";
import { customBinding } from "./custom.js";
import type { Grant, SSTInclude } from "./grants.js";
import { type DescribedPostgres, postgresBinding } from "./postgres.js";

/** An SST component, as SST already describes itself to its own binding consumers. */
export interface SSTPostgresBindable {
  getSSTBinding(): {
    properties: Record<string, unknown>;
    include?: SSTInclude[];
  };
}

type Input<T> = T | Promise<T> | { apply(f: (value: T) => unknown): unknown };

/**
 * A postgres resource described by hand, for anything SST does not describe.
 *
 * The fields are the ones `common.bindings.v1.PostgresProperties` declares, and the grants
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

/** Where a binding lands: an ocel class, one preview environment, and the project holding both. */
export interface BindOptions {
  class?: "production" | "preview";
  environment?: string;
  project?: string;
}

interface BindingInputs extends Target {
  name: string;
  owner: string;
}

interface PostgresInputs extends BindingInputs, DescribedPostgres {}

interface CustomInputs extends BindingInputs {
  properties: Record<string, unknown>;
}

interface BindingState extends Target {
  name: string;
  owner: string;
  digest: string;
}

function bindingProvider<I extends BindingInputs>(
  recordFor: (inputs: I) => object,
  resolved: (inputs: I) => boolean,
) {
  const digestOf = (inputs: I) =>
    crypto
      .createHash("sha256")
      .update(JSON.stringify(recordFor(inputs)))
      .digest("hex");

  const stateOf = (inputs: I): BindingState => ({
    name: inputs.name,
    owner: inputs.owner,
    project: inputs.project,
    class: inputs.class,
    environment: inputs.environment,
    digest: digestOf(inputs),
  });

  const set = (inputs: I) =>
    runBindings(["set", "--owner", inputs.owner], inputs, `${JSON.stringify(recordFor(inputs))}\n`);

  return {
    async create(inputs: I) {
      set(inputs);
      return { id: idFor(inputs), outs: stateOf(inputs) };
    },

    async diff(_id: string, olds: BindingState, news: I) {
      const replaces = replacesFor(olds, news);
      return {
        changes: replaces.length > 0 || !resolved(news) || olds.digest !== digestOf(news),
        replaces,
        deleteBeforeReplace: replaces.length > 0,
      };
    },

    async update(_id: string, _olds: BindingState, news: I) {
      set(news);
      return { outs: stateOf(news) };
    },

    async delete(_id: string, props: BindingState) {
      runBindings(["rm", props.name], props);
    },
  };
}

export const postgresProvider = bindingProvider<PostgresInputs>(
  (inputs) =>
    postgresBinding(inputs.name, {
      properties: inputs.properties,
      include: inputs.include,
      grants: inputs.grants,
    }),
  (inputs) => bindingFields.every((field) => inputs.properties[field] !== undefined),
);

export const customProvider = bindingProvider<CustomInputs>(
  (inputs) => customBinding(inputs.name, inputs.properties),
  (inputs) => Object.values(inputs.properties).every((value) => value !== undefined),
);

/**
 * Publishes one SST-defined resource as one ocel binding, as a side effect of this apply.
 *
 * The name is this app's own, published for an ocel app's config to bind a
 * declared resource to — `postgres("sst-pg-orders", …)` here, and
 * `"bindings": { "postgres": { "orders": "sst-pg-orders" } }` in `ocel.json` —
 * and the resource is either an SST component, whose own binding description is
 * passed through, or the postgres fields written out by hand. `class` defaults to production, `environment` names one preview
 * environment, and `project` is the directory holding `ocel.json`, which is
 * the SST config root unless it is given.
 */
export function postgres(
  name: string,
  resource: SSTPostgresBindable | DescribedPostgresResource,
  opts?: BindOptions,
): void {
  const util = host();
  const target: Target = {
    project: opts?.project ?? configRoot(),
    class: opts?.class ?? "production",
    environment: opts?.environment,
  };
  checkTarget(target);

  const logical = `ocel-binding-${name}`;
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
 * Ocel neither types nor interprets what a custom binding carries — it hands the
 * values to a transform that fills a surface field with them, so nothing is
 * delivered to an app and no grants are accepted.
 */
export type CustomProperties = Record<string, Input<unknown>>;

/**
 * Publishes one set of values your own infrastructure holds as one ocel custom
 * binding, as a side effect of this apply.
 *
 * The name is the one a transform reads — `bind.custom("network", …)` here,
 * `bindings.custom.network.subnetIds` in a transform module. `class` defaults to
 * production, `environment` names one preview environment, and `project` is the
 * directory holding `ocel.json`, which is the SST config root unless it is
 * given.
 */
export function custom(name: string, properties: CustomProperties, opts?: BindOptions): void {
  const util = host();
  const target: Target = {
    project: opts?.project ?? configRoot(),
    class: opts?.class ?? "production",
    environment: opts?.environment,
  };
  checkTarget(target);

  const logical = `ocel-binding-${name}`;
  new util.dynamic.Resource(customProvider, logical, {
    ...target,
    name,
    owner: ownerFor(util, logical),
    properties,
  });
}

function idFor(inputs: BindingInputs): string {
  return [inputs.class, inputs.environment, inputs.name].filter(Boolean).join("/");
}

const identity = ["name", "owner", "project", "class", "environment"] as const;

function replacesFor(olds: BindingState, news: BindingInputs): string[] {
  return identity.filter((field) => olds[field] !== news[field]);
}

const bindingFields = ["host", "port", "database", "username", "password"] as const;

function describe(resource: SSTPostgresBindable | DescribedPostgresResource): DescribedPostgres {
  if (typeof (resource as SSTPostgresBindable).getSSTBinding === "function") {
    const described = (resource as SSTPostgresBindable).getSSTBinding();
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
  for (const field of bindingFields) {
    out[field] = properties[field];
  }
  return out;
}

interface DynamicHost {
  getStack(): string;
  getProject(): string;
  dynamic: {
    Resource: new (provider: unknown, name: string, props: Record<string, unknown>) => unknown;
  };
}

const dynamicType = "pulumi:pulumi:Stack$pulumi-nodejs:dynamic:Resource";

function ownerFor(util: DynamicHost, logical: string): string {
  return [`urn:pulumi:${util.getStack()}`, util.getProject(), dynamicType, logical].join("::");
}

declare const $util: DynamicHost | undefined;

declare const $cli: { paths?: { root?: string } } | undefined;

function host(): DynamicHost {
  const util = typeof $util === "undefined" ? undefined : $util;
  if (!util?.dynamic?.Resource) {
    throw new Error(
      "@ocel/sst binds from inside the run() of your sst.config.ts, where SST supplies $util; nothing supplies it here. Move the binding call into run().",
    );
  }
  return util;
}

function configRoot(): string {
  const cli = typeof $cli === "undefined" ? undefined : $cli;
  const root = cli?.paths?.root;
  if (!root) {
    throw new Error(
      "@ocel/sst publishes into the ocel project holding ocel.json, and SST named no config root here; pass `project` to say which directory holds it",
    );
  }
  return root;
}
