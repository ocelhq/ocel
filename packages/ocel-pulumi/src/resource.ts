import crypto from "node:crypto";
import path from "node:path";
import { createUrn, dynamic, type Input, isSecret, output, type Resource } from "@pulumi/pulumi";
import { checkTarget, runBindings, type Target } from "./cli.js";
import { publishedCustom } from "./custom.js";
import type { GrantInput } from "./grants.js";
import { type DescribedPostgres, publishedPostgres } from "./postgres.js";

/**
 * A postgres resource described from what a Pulumi resource exposes.
 *
 * The fields are the ones `common.bindings.v1.PostgresProperties` declares, each of them
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
  grants?: GrantInput[];
}

/**
 * The values a custom binding publishes, written out by hand under a name the
 * ocel project reads: a transform as `bindings.custom.<name>.<property>`, and an
 * app's declared variable through `"bindings": { "variables": { <KEY>: "@<name>.<property>" } }`.
 *
 * Ocel neither types nor interprets what a custom binding contains, so no grants
 * are accepted. A property given as a secret output is published marked secret,
 * and only an app's `secret` variable may take it.
 */
export type CustomProperties = Record<string, Input<unknown>>;

/**
 * Where a binding lands: an ocel tier, one preview environment, the project
 * containing both, and the Pulumi resource this one hangs under.
 */
export interface BindOptions {
  tier?: "production" | "preview";
  environment?: string;
  project?: string;
  parent?: Resource;
}

interface BindingInputs extends Target {
  name: string;
  owner: string;
  directory: string;
}

interface PostgresInputs extends BindingInputs, DescribedPostgres {}

interface CustomInputs extends BindingInputs {
  properties: Record<string, unknown>;
  secrets: string[];
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
    tier: inputs.tier,
    environment: inputs.environment,
    digest: digestOf(inputs),
  });

  const set = (inputs: I) =>
    runBindings(
      ["set", "--owner", inputs.owner],
      inputs,
      inputs.directory,
      `${JSON.stringify(recordFor(inputs))}\n`,
    );

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
      runBindings(["rm", props.name], props, path.resolve(process.cwd(), props.project));
    },
  };
}

export const postgresProvider = bindingProvider<PostgresInputs>(
  (inputs) =>
    publishedPostgres(inputs.name, {
      properties: inputs.properties,
      grants: inputs.grants,
    }),
  (inputs) => bindingFields.every((field) => inputs.properties[field] !== undefined),
);

export const customProvider = bindingProvider<CustomInputs>(
  (inputs) => publishedCustom(inputs.name, inputs.properties, inputs.secrets),
  (inputs) => Object.values(inputs.properties).every((value) => value !== undefined),
);

/**
 * Publishes one Pulumi-defined resource as one ocel binding, as a side effect of
 * this update.
 *
 * The name is this program's own, published for an app's config to bind a
 * declared resource to — `postgres("pulumi-pg-orders", …)` here, and
 * `"bindings": { "postgres": { "orders": "@pulumi-pg-orders" } }` in `ocel.json`
 * — and the resource is the postgres fields read off whatever provisioned it,
 * each of them an output this update resolves.
 * `tier` defaults to production, `environment` names one preview environment,
 * and `project` is the directory containing `ocel.json`, which is the
 * directory Pulumi runs the program from unless it is given.
 */
export function postgres(
  name: string,
  resource: DescribedPostgresResource,
  opts?: BindOptions,
): void {
  declare(postgresProvider, name, opts, describe(resource));
}

/**
 * Publishes one set of values from your own infrastructure as one ocel custom
 * binding, as a side effect of this update.
 *
 * The name is the one the ocel project reads — `bind.custom("network", …)` here,
 * `bindings.custom.network.subnetIds` in a transform module, and
 * `"bindings": { "variables": { "SUBNET_IDS": "@network.subnetIds" } }` for an
 * app's declared variable. A property given as a secret output, such as
 * `config.requireSecret(…)` or `pulumi.secret(…)`, is published marked secret and
 * reaches only a `secret` variable. `tier` defaults to production, `environment`
 * names one preview environment, and `project` is the directory containing
 * `ocel.json`, which is the directory Pulumi runs the program from unless it is
 * given.
 */
export function custom(name: string, properties: CustomProperties, opts?: BindOptions): void {
  declare(customProvider, name, opts, { properties, secrets: secretProperties(properties) });
}

async function secretProperties(properties: CustomProperties): Promise<string[]> {
  const marked = await Promise.all(
    Object.entries(properties).map(async ([key, value]) =>
      (await isSecret(output(value))) ? key : undefined,
    ),
  );
  return marked.filter((key) => key !== undefined).sort();
}

const dynamicType = "pulumi-nodejs:dynamic:Resource";

function declare(
  provider: dynamic.ResourceProvider,
  name: string,
  opts: BindOptions | undefined,
  described: object,
): void {
  const directory = path.resolve(opts?.project ?? process.cwd());
  const target: Target = {
    project: path.relative(process.cwd(), directory) || ".",
    tier: opts?.tier ?? "production",
    environment: opts?.environment,
  };
  checkTarget(target);

  const logical = `ocel-binding-${name}`;
  new BindingResource(
    provider,
    logical,
    {
      ...target,
      directory,
      name,
      owner: createUrn(logical, dynamicType, opts?.parent),
      ...described,
    },
    { parent: opts?.parent, additionalSecretOutputs: ["digest"] },
  );
}

class BindingResource extends dynamic.Resource {}

function idFor(inputs: BindingInputs): string {
  return [inputs.tier, inputs.environment, inputs.name].filter(Boolean).join("/");
}

const identity = ["name", "owner", "project", "tier", "environment"] as const;

function replacesFor(olds: BindingState, news: BindingInputs): string[] {
  return identity.filter((field) => olds[field] !== news[field]);
}

const bindingFields = ["host", "port", "database", "username", "password"] as const;

function describe(resource: DescribedPostgresResource) {
  return {
    properties: pick(resource as unknown as Record<string, unknown>),
    grants: resource.grants,
  };
}

function pick(properties: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const field of bindingFields) {
    out[field] = properties[field];
  }
  return out;
}
