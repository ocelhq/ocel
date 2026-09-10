import { awsOwnedFields } from "./aws";
import {
  type EnvClass,
  type GateContext,
  reservedTagPrefix,
  ruleKeywords,
  type TagMap,
  type TransformDefinition,
  type TransformRule,
} from "./define";
import type { TransformBindings } from "./index";
import { bindings, isBindingOutput } from "./output";

/**
 * What one ocel resource's rules came to: a patch per underlying resource the
 * provider constructs for it, keyed by the same key the rules were written
 * under and carrying the provider SDK's own property names.
 */
export type Patches = Record<string, Record<string, unknown>>;

/** One resource the deploy offers the modules, as the deploy addresses it. */
export interface RequestResource {
  readonly type: string;
  readonly name: string;
  readonly app?: string;
}

/** What a deploy asks the modules about: the environment, and every candidate in it. */
export interface EvaluateRequest {
  readonly provider: string;
  readonly envClass: EnvClass;
  readonly env: string;
  readonly resources: readonly RequestResource[];
}

/** What the modules came to for one candidate, in the order the request listed it. */
export interface EvaluatedResource {
  readonly name: string;
  readonly patches: Patches;
  readonly tags: TagMap;
}

/** The answer to one request, one entry per candidate the request carried. */
export interface EvaluateResponse {
  readonly resources: readonly EvaluatedResource[];
}

/** A loaded module, paired with the specifier a refusal names it by. */
export interface TransformModule {
  readonly specifier: string;
  readonly definition: TransformDefinition;
}

const ownedFields: Record<string, Record<string, Record<string, readonly string[]>>> = {
  aws: awsOwnedFields,
};

interface LoadedModule {
  readonly specifier: string;
  readonly rules: readonly TransformRule[];
}

/**
 * Applies the modules in order to every candidate the deploy offers, later
 * modules winning. A rule that names a field ocel owns, a resource this
 * provider does not construct, or a provider this project does not deploy to
 * throws where it was written, and the deploy stops.
 */
export function evaluate(
  request: EvaluateRequest,
  modules: readonly TransformModule[],
): EvaluateResponse {
  const loaded = modules.map((module) => load(request, module));
  return {
    resources: request.resources.map((resource) => evaluateResource(request, resource, loaded)),
  };
}

function load(request: EvaluateRequest, module: TransformModule): LoadedModule {
  const rules = module.definition.rules({
    bindings: bindings as TransformBindings,
    envClass: request.envClass,
    env: request.env,
  });
  const branches = new Set<string>();
  for (const rule of rules) {
    for (const key of Object.keys(rule)) {
      if (ruleKeywords.includes(key as (typeof ruleKeywords)[number])) continue;
      branches.add(key);
    }
  }
  if (!branches.has(request.provider)) {
    throw new Error(
      `${module.specifier} patches ${listed(branches)} and this project deploys to ${request.provider}; give the module an ${request.provider} branch, or drop it from "transforms"`,
    );
  }
  return { specifier: module.specifier, rules };
}

function listed(branches: Set<string>): string {
  const names = [...branches].sort();
  if (names.length === 0) return "no provider";
  return names.join(", ");
}

function evaluateResource(
  request: EvaluateRequest,
  resource: RequestResource,
  modules: readonly LoadedModule[],
): EvaluatedResource {
  const patches: Patches = {};
  const tags: TagMap = {};
  const ctx = Object.freeze<GateContext>({
    envClass: request.envClass,
    env: request.env,
    app: resource.app,
  });

  for (const module of modules) {
    for (const rule of module.rules) {
      checkRuleKeys(module.specifier, rule, request.provider);
      if (rule.if !== undefined && !rule.if(ctx)) continue;
      collectTags(module.specifier, rule.tags, tags);

      const branch = (rule as Record<string, unknown>)[request.provider];
      if (branch === undefined) continue;
      const group = readGroup(module.specifier, request.provider, branch, resource.type);
      if (group === undefined) continue;
      for (const [key, patch] of Object.entries(group)) {
        if (patch === undefined) continue;
        applyPatch(module.specifier, request.provider, resource.type, key, patch, patches);
      }
    }
  }

  return { name: resource.name, patches, tags };
}

function checkRuleKeys(specifier: string, rule: TransformRule, provider: string): void {
  for (const key of Object.keys(rule)) {
    if (ruleKeywords.includes(key as (typeof ruleKeywords)[number])) continue;
    if (key === provider) continue;
    if (Object.hasOwn(ownedFields, key)) continue;
    throw new Error(
      `${specifier}: a rule targets ${key}, which is neither a provider nor ${ruleKeywords.join(", ")}`,
    );
  }
}

function collectTags(specifier: string, authored: TagMap | undefined, tags: TagMap): void {
  if (authored === undefined) return;
  for (const [key, value] of Object.entries(authored)) {
    if (key.startsWith(reservedTagPrefix)) {
      throw new Error(
        `${specifier}: tag ${key} is ocel's own — the ${reservedTagPrefix} prefix is reserved`,
      );
    }
    tags[key] = value;
  }
}

function readGroup(
  specifier: string,
  provider: string,
  branch: unknown,
  type: string,
): Record<string, unknown> | undefined {
  const known = ownedFields[provider];
  if (known === undefined) {
    throw new Error(`${specifier}: this build renders nothing for ${provider}`);
  }
  const rendered = branch as Record<string, unknown>;
  for (const key of Object.keys(rendered)) {
    if (!Object.hasOwn(known, key)) {
      throw new Error(
        `${specifier}: a rule patches ${provider}.${key}, which is not a resource this provider renders (it renders ${Object.keys(known).join(", ")})`,
      );
    }
  }
  const group = rendered[type];
  return group === undefined ? undefined : (group as Record<string, unknown>);
}

function applyPatch(
  specifier: string,
  provider: string,
  type: string,
  key: string,
  patch: unknown,
  into: Patches,
): void {
  const owned = ownedFields[provider]?.[type]?.[key];
  if (owned === undefined) {
    const rendered = Object.keys(ownedFields[provider]?.[type] ?? {}).join(", ");
    throw new Error(
      `${specifier}: a rule patches ${provider}.${type}.${key}, which this provider does not construct (it constructs ${rendered})`,
    );
  }
  const fields = patch as Record<string, unknown>;
  for (const field of Object.keys(fields)) {
    if (owned.includes(field)) {
      throw new Error(
        `${specifier}: a rule sets ${provider}.${type}.${key}.${field}, which ocel fills from what this deploy built; drop it and let ocel own it`,
      );
    }
  }
  into[key] = mergePatch(into[key] ?? {}, fields) as Record<string, unknown>;
}

function mergePatch(base: unknown, over: unknown): unknown {
  if (!plainObject(base) || !plainObject(over)) return over;
  const merged: Record<string, unknown> = { ...base };
  for (const [key, value] of Object.entries(over)) {
    merged[key] = mergePatch(merged[key], value);
  }
  return merged;
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return (
    typeof value === "object" && value !== null && !Array.isArray(value) && !isBindingOutput(value)
  );
}
