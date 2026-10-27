import crypto from "node:crypto";
import path from "node:path";
import {
  dynamic,
  getProject,
  getStack,
  type Input,
  isSecret,
  output,
  type Resource,
  runtime,
} from "@pulumi/pulumi";
import { checkTarget, runOcel, type Target } from "./cli.js";
import { publishedCustom } from "./custom.js";
import type { GrantInput } from "./grants.js";
import { type DescribedPostgres, publishedPostgres } from "./postgres.js";
import { publishedValues, publishedVersions, writtenVersions } from "./variables.js";

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
 * A record only transforms read: the properties written out by hand, under a
 * name a transform names.
 *
 * Ocel neither types nor interprets what a custom binding contains — it hands the
 * values to a transform that fills a surface field with them, so nothing is
 * delivered to an app and no grants are accepted.
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
}

interface BindingState extends Target {
  name: string;
  owner: string;
  version: string;
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

  const set = (inputs: I): BindingState => {
    const result = runOcel(
      ["bindings", "set", "--owner", inputs.owner],
      inputs,
      inputs.directory,
      `${JSON.stringify(recordFor(inputs))}\n`,
    ) as { version?: string } | undefined;
    if (!result?.version) {
      throw new Error(
        `ocel bindings set reported no version for ${inputs.name}, and removing this call removes only the version it wrote; run an ocel CLI that reports it`,
      );
    }
    return {
      name: inputs.name,
      owner: inputs.owner,
      ...definedTarget(inputs),
      version: result.version,
      digest: digestOf(inputs),
    };
  };

  return {
    async create(inputs: I) {
      return { id: idFor(inputs), outs: set(inputs) };
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
      return { outs: set(news) };
    },

    async delete(_id: string, props: BindingState) {
      runOcel(
        ["bindings", "rm", props.name, "--at-version", props.version],
        props,
        path.resolve(process.cwd(), props.project),
      );
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
  (inputs) => publishedCustom(inputs.name, inputs.properties),
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
 * The name is the one a transform reads — `bind.custom("network", …)` here,
 * `bindings.custom.network.subnetIds` in a transform module. `tier` defaults to
 * production, `environment` names one preview environment, and `project` is the
 * directory containing `ocel.json`, which is the directory Pulumi runs the
 * program from unless it is given.
 */
export function custom(name: string, properties: CustomProperties, opts?: BindOptions): void {
  declare(customProvider, name, opts, { properties });
}

/**
 * The variables a program publishes, by the name an app declares each under:
 * text as it is, and a number, boolean, list or object as JSON. Each value is
 * an `Input`, resolved before it is published.
 */
export type VariableValues = Record<string, Input<unknown>>;

interface VariablesInputs extends Target {
  directory: string;
  publisher: string;
  values: Record<string, unknown>;
  secrets: string[];
}

interface VariablesState extends Target {
  publisher: string;
  versions: Record<string, string>;
  digest: string;
}

const publishedRecord = (inputs: VariablesInputs) => publishedValues(inputs.values, inputs.secrets);

const variablesDigest = (inputs: VariablesInputs) =>
  crypto
    .createHash("sha256")
    .update(JSON.stringify(publishedRecord(inputs)))
    .digest("hex");

const publishVariables = (inputs: VariablesInputs): VariablesState => {
  const record = publishedRecord(inputs);
  const result = runOcel(
    ["env", "publish", "--publisher", inputs.publisher],
    inputs,
    inputs.directory,
    `${JSON.stringify(record)}\n`,
  );
  return {
    publisher: inputs.publisher,
    ...definedTarget(inputs),
    versions: writtenVersions(record, result),
    digest: variablesDigest(inputs),
  };
};

const variablesIdentity = ["publisher", "project", "tier", "environment"] as const;

export const variablesProvider = {
  async create(inputs: VariablesInputs) {
    return {
      id: ["variables", inputs.tier, inputs.environment].filter(Boolean).join("/"),
      outs: publishVariables(inputs),
    };
  },

  async diff(_id: string, olds: VariablesState, news: VariablesInputs) {
    const replaces = variablesIdentity.filter((field) => olds[field] !== news[field]);
    const resolved = Object.values(news.values).every((value) => value !== undefined);
    return {
      changes: replaces.length > 0 || !resolved || olds.digest !== variablesDigest(news),
      replaces,
      deleteBeforeReplace: replaces.length > 0,
    };
  },

  async update(_id: string, _olds: VariablesState, news: VariablesInputs) {
    return { outs: publishVariables(news) };
  },

  async delete(_id: string, props: VariablesState) {
    runOcel(
      ["env", "unpublish", "--publisher", props.publisher],
      props,
      path.resolve(process.cwd(), props.project),
      `${JSON.stringify(publishedVersions(props.versions))}\n`,
    );
  },
};

/**
 * Publishes values from your own infrastructure as variables an app reads
 * through `ocel/env`, as a side effect of this update.
 *
 * Each key is the name an app declares — `bind.variables({ API_KEY: key.result })`
 * here, `defineEnv({ API_KEY: { class: "secret" } })` in the app — and the
 * declaration is checked as `ocel env set` checks it. A value given as a secret
 * output, such as `config.requireSecret(…)` or `pulumi.secret(…)`, is published
 * as a secret and must be declared `secret`; any other value must not be.
 *
 * The values belong to this program, by its stack and project, wherever the call
 * hangs: `ocel env set` and `ocel env rm` refuse them, the next update rewrites
 * them, a name the call stops listing is removed, and removing the call removes
 * the values it wrote unless another call has written them since. Call it once
 * per coordinate; a second call for one is refused. `tier`
 * defaults to production, `environment` names one preview environment, and
 * `project` is the directory containing `ocel.json`, which is the directory
 * Pulumi runs the program from unless it is given.
 */
export function variables(values: VariableValues, opts?: BindOptions): void {
  const { target, directory } = targetFor(opts);
  refuseSecondCall(`bind.variables is called twice for ${describeTarget(target)}`, target);
  new VariablesResource(
    variablesProvider,
    logicalName("ocel-variables", target),
    {
      ...target,
      directory,
      publisher: programID(),
      values,
      secrets: secretKeys(values),
    },
    { parent: opts?.parent, additionalSecretOutputs: ["digest"] },
  );
}

async function secretKeys(values: VariableValues): Promise<string[]> {
  const marked = await Promise.all(
    Object.entries(values).map(async ([key, value]) =>
      (await isSecret(output(value))) ? key : undefined,
    ),
  );
  return marked.filter((key) => key !== undefined).sort();
}

class VariablesResource extends dynamic.Resource {}

function programID(): string {
  return `urn:pulumi:${getStack()}::${getProject()}`;
}

const calls = new WeakMap<object, Set<string>>();

function refuseSecondCall(refusal: string, target: Target, name = ""): void {
  const stack = runtime.getStackResource();
  if (!stack) {
    return;
  }
  const made = calls.get(stack) ?? new Set<string>();
  calls.set(stack, made);
  const call = coordinate(target, name);
  if (made.has(call)) {
    throw new Error(
      `${refusal}: every call publishes as this program, so the second would replace what the first published. Make one call for it`,
    );
  }
  made.add(call);
}

function coordinate(target: Target, name = ""): string {
  return JSON.stringify([target.project, target.tier, target.environment ?? "", name]);
}

function logicalName(kind: string, target: Target, name = ""): string {
  const readable = [kind, target.tier, target.environment, name].filter(Boolean).join("-");
  const digest = crypto
    .createHash("sha256")
    .update(coordinate(target, name))
    .digest("hex")
    .slice(0, 8);
  return `${readable}-${digest}`;
}

function describeTarget(target: Target): string {
  const at = target.environment ? `${target.tier} environment ${target.environment}` : target.tier;
  return `${at} of ocel project ${target.project}`;
}

function definedTarget({ project, tier, environment }: Target): Target {
  return environment === undefined ? { project, tier } : { project, tier, environment };
}

function targetFor(opts: BindOptions | undefined): { target: Target; directory: string } {
  const directory = path.resolve(opts?.project ?? process.cwd());
  const target: Target = {
    project: path.relative(process.cwd(), directory) || ".",
    tier: opts?.tier ?? "production",
    environment: opts?.environment,
  };
  checkTarget(target);
  return { target, directory };
}

function declare(
  provider: dynamic.ResourceProvider,
  name: string,
  opts: BindOptions | undefined,
  described: object,
): void {
  const { target, directory } = targetFor(opts);
  refuseSecondCall(
    `binding ${name} is published twice for ${describeTarget(target)}`,
    target,
    name,
  );

  new BindingResource(
    provider,
    logicalName("ocel-binding", target, name),
    { ...target, directory, name, owner: programID(), ...described },
    { parent: opts?.parent },
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
