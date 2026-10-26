import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { checkTarget, runOcel, type Target } from "./cli.js";
import { publishedCustom } from "./custom.js";
import type { GrantInput, SSTInclude } from "./grants.js";
import type { Input } from "./input.js";
import { type DescribedPostgres, publishedPostgres } from "./postgres.js";
import { publishedValues, publishedVersions, writtenVersions } from "./variables.js";

/** An SST component, as SST links it to its own consumers: its properties and the permissions it includes. */
export interface SSTPostgresBindable {
  getSSTLink(): {
    properties: Record<string, unknown>;
    include?: { type: string; [key: string]: unknown }[];
  };
}

/** A resource in the SST app that what is published hangs under, by its URN. */
export interface ParentResource {
  readonly urn: Input<string>;
}

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
  grants?: GrantInput[];
}

/**
 * Where a binding lands: an ocel tier, one preview environment, the project
 * containing both, and the resource in the SST app this one hangs under.
 */
export interface BindOptions {
  tier?: "production" | "preview";
  environment?: string;
  project?: string;
  parent?: ParentResource;
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
      project: inputs.project,
      tier: inputs.tier,
      environment: inputs.environment,
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
        path.resolve(configRoot(), props.project),
      );
    },
  };
}

export const postgresProvider = bindingProvider<PostgresInputs>(
  (inputs) =>
    publishedPostgres(inputs.name, {
      properties: inputs.properties,
      include: inputs.include,
      grants: inputs.grants,
    }),
  (inputs) => bindingFields.every((field) => inputs.properties[field] !== undefined),
);

export const customProvider = bindingProvider<CustomInputs>(
  (inputs) => publishedCustom(inputs.name, inputs.properties),
  (inputs) => Object.values(inputs.properties).every((value) => value !== undefined),
);

/**
 * Publishes one SST-defined resource as one ocel binding, as a side effect of this apply.
 *
 * The name is this app's own, published for an ocel app's config to bind a
 * declared resource to — `postgres("sst-pg-orders", …)` here, and
 * `"bindings": { "postgres": { "orders": "@sst-pg-orders" } }` in `ocel.json` —
 * and the resource is either an SST component, whose own link is passed
 * through with the permissions it includes, or the postgres fields written out by hand. `tier` defaults to production, `environment` names one preview
 * environment, and `project` is the directory containing `ocel.json`, which is
 * the SST config root unless it is given.
 */
export function postgres(
  name: string,
  resource: SSTPostgresBindable | DescribedPostgresResource,
  opts?: BindOptions,
): void {
  declare(postgresProvider, name, opts, describe(resource));
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
 * Publishes one set of values from your own infrastructure as one ocel custom
 * binding, as a side effect of this apply.
 *
 * The name is the one a transform reads — `bind.custom("network", …)` here,
 * `bindings.custom.network.subnetIds` in a transform module. `tier` defaults to
 * production, `environment` names one preview environment, and `project` is the
 * directory containing `ocel.json`, which is the SST config root unless it is
 * given.
 */
export function custom(name: string, properties: CustomProperties, opts?: BindOptions): void {
  declare(customProvider, name, opts, { properties });
}

/**
 * The variables an SST app publishes, by the name an app declares each under:
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
    project: inputs.project,
    tier: inputs.tier,
    environment: inputs.environment,
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
      path.resolve(configRoot(), props.project),
      `${JSON.stringify(publishedVersions(props.versions))}\n`,
    );
  },
};

/**
 * Publishes values from your own infrastructure as variables an app reads
 * through `ocel/env`, as a side effect of this apply.
 *
 * Each key is the name an app declares — `bind.variables({ API_KEY: key.result })`
 * here, `defineEnv({ API_KEY: { class: "secret" } })` in the app — and the
 * declaration is checked as `ocel env set` checks it. A value given as a secret
 * output, such as a `sst.Secret`'s value or `$util.secret(…)`, is published as a
 * secret and must be declared `secret`; any other value must not be.
 *
 * The values belong to this app's stage, wherever the call hangs: `ocel env set`
 * and `ocel env rm` refuse them, the next apply rewrites them, a name the call
 * stops listing is removed, and removing the call removes the values it wrote
 * unless another call has written them since. Call it once per coordinate; a
 * second call for one is refused. `tier`
 * defaults to production, `environment` names one preview environment, and
 * `project` is the directory containing `ocel.json`, which is the SST config
 * root unless it is given.
 */
export function variables(values: VariableValues, opts?: BindOptions): void {
  const util = host();
  const { isSecret, output } = util;
  if (!isSecret || !output) {
    throw new Error(
      "@ocel/sst marks a secret output with $util.isSecret, and the $util SST supplies here has none, so a secret would be published as plain text; run this from the run() of an sst.config.ts on SST v3",
    );
  }
  const located = targetFor(opts);
  const { target } = located;
  refuseSecondCall(util, `bind.variables is called twice for ${describeTarget(target)}`, target);
  declareAs(
    util,
    variablesProvider,
    logicalName("ocel-variables", target),
    located,
    opts,
    {
      publisher: programID(util),
      values,
      secrets: secretKeys(values, (value) => isSecret(output(value))),
    },
    ["digest"],
  );
}

async function secretKeys(
  values: VariableValues,
  isSecret: (value: unknown) => Promise<boolean>,
): Promise<string[]> {
  const marked = await Promise.all(
    Object.entries(values).map(async ([key, value]) => ((await isSecret(value)) ? key : undefined)),
  );
  return marked.filter((key) => key !== undefined).sort();
}

function declare(
  provider: unknown,
  name: string,
  opts: BindOptions | undefined,
  described: object,
): void {
  const util = host();
  const located = targetFor(opts);
  refuseSecondCall(
    util,
    `binding ${name} is published twice for ${describeTarget(located.target)}`,
    located.target,
    name,
  );
  const logical = logicalName("ocel-binding", located.target, name);
  declareAs(util, provider, logical, located, opts, {
    name,
    owner: programID(util),
    ...described,
  });
}

function declareAs(
  util: DynamicHost,
  provider: unknown,
  logical: string,
  { target, directory }: { target: Target; directory: string },
  opts: BindOptions | undefined,
  props: object,
  additionalSecretOutputs?: string[],
): void {
  new util.dynamic.Resource(
    provider,
    logical,
    { ...target, directory, ...props },
    { parent: opts?.parent, additionalSecretOutputs },
  );
}

function programID(util: DynamicHost): string {
  return `urn:pulumi:${util.getStack()}::${util.getProject()}`;
}

const calls = new WeakMap<object, Set<string>>();

function refuseSecondCall(util: DynamicHost, refusal: string, target: Target, name = ""): void {
  const stack = util.runtime?.getStackResource?.();
  if (!stack) {
    return;
  }
  const made = calls.get(stack) ?? new Set<string>();
  calls.set(stack, made);
  const call = coordinate(target, name);
  if (made.has(call)) {
    throw new Error(
      `${refusal}: every call publishes as this app's stage, so the second would replace what the first published. Make one call for it`,
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

function targetFor(opts: BindOptions | undefined): { target: Target; directory: string } {
  const root = configRoot();
  const directory = path.resolve(root, opts?.project ?? root);
  const target: Target = {
    project: path.relative(root, directory) || ".",
    tier: opts?.tier ?? "production",
    environment: opts?.environment,
  };
  checkTarget(target);
  return { target, directory };
}

function idFor(inputs: BindingInputs): string {
  return [inputs.tier, inputs.environment, inputs.name].filter(Boolean).join("/");
}

const identity = ["name", "owner", "project", "tier", "environment"] as const;

function replacesFor(olds: BindingState, news: BindingInputs): string[] {
  return identity.filter((field) => olds[field] !== news[field]);
}

const bindingFields = ["host", "port", "database", "username", "password"] as const;

function describe(resource: SSTPostgresBindable | DescribedPostgresResource) {
  if ("getSSTLink" in resource) {
    const link = resource.getSSTLink();
    return {
      properties: pick(link.properties),
      include: link.include as SSTInclude[] | undefined,
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
  isSecret?(value: unknown): Promise<boolean>;
  runtime?: { getStackResource?(): object | undefined };
  output?(value: unknown): unknown;
  dynamic: {
    Resource: new (
      provider: unknown,
      name: string,
      props: Record<string, unknown>,
      opts?: Record<string, unknown>,
    ) => unknown;
  };
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
  const root = cli?.paths?.root ?? configAbove(process.cwd());
  if (!root) {
    throw new Error(
      `@ocel/sst places an ocel project relative to the SST config root, and SST named none here, nor is there an ${sstConfig} above ${process.cwd()}; bind from inside the run() of your ${sstConfig}`,
    );
  }
  return root;
}

const sstConfig = "sst.config.ts";

function configAbove(start: string): string | undefined {
  for (let dir = start; ; dir = path.dirname(dir)) {
    if (fs.existsSync(path.join(dir, sstConfig))) {
      return dir;
    }
    if (path.dirname(dir) === dir) {
      return undefined;
    }
  }
}
