import { ruleKeywords, type TransformRule } from "./define";
import {
  allowedFields,
  isSurfaceType,
  reservedTagPrefix,
  surfaceFields,
  type EnvClass,
  type GateContext,
  type SurfaceType,
  type TagMap,
} from "./surface";

export type Surfaces = Record<string, Record<string, unknown>>;

export interface RequestResource {
  readonly type: SurfaceType;
  readonly name: string;
  readonly app?: string;
  readonly surfaces: Surfaces;
}

export interface EvaluateRequest {
  readonly envClass: EnvClass;
  readonly env: string;
  readonly resources: readonly RequestResource[];
}

export interface EvaluatedResource {
  readonly name: string;
  readonly surfaces: Surfaces;
  readonly tags: TagMap;
}

export interface EvaluateResponse {
  readonly resources: readonly EvaluatedResource[];
}

export interface TransformModule {
  readonly specifier: string;
  readonly rules: readonly TransformRule[];
}

export function evaluate(
  request: EvaluateRequest,
  modules: readonly TransformModule[],
): EvaluateResponse {
  for (const module of modules) {
    for (const rule of module.rules) {
      checkRuleKeys(module.specifier, rule);
    }
  }
  return {
    resources: request.resources.map((resource) =>
      evaluateResource(request, resource, modules),
    ),
  };
}

function checkRuleKeys(specifier: string, rule: TransformRule): void {
  for (const key of Object.keys(rule)) {
    if (ruleKeywords.includes(key as (typeof ruleKeywords)[number])) continue;
    if (isSurfaceType(key)) continue;
    throw new Error(
      `${specifier}: a rule targets ${key}, which is neither a resource this provider renders (${Object.keys(surfaceFields).join(", ")}) nor ${ruleKeywords.join(", ")}`,
    );
  }
}

function evaluateResource(
  request: EvaluateRequest,
  resource: RequestResource,
  modules: readonly TransformModule[],
): EvaluatedResource {
  if (!isSurfaceType(resource.type)) {
    throw new Error(
      `${resource.name}: this provider renders no transformable resource of type ${resource.type}`,
    );
  }

  const surfaces: Surfaces = {};
  for (const [key, args] of Object.entries(resource.surfaces)) {
    surfaces[key] = { ...args };
  }
  const tags: TagMap = {};

  const ctx = Object.freeze<GateContext>({
    envClass: request.envClass,
    env: request.env,
    app: resource.app,
  });

  for (const module of modules) {
    for (const rule of module.rules) {
      if (rule.if !== undefined && !rule.if(ctx)) continue;

      if (rule.tags !== undefined) {
        for (const [key, value] of Object.entries(rule.tags)) {
          if (key.startsWith(reservedTagPrefix)) {
            throw new Error(
              `${module.specifier}: tag ${key} is ocel's own — the ${reservedTagPrefix} prefix is reserved`,
            );
          }
          tags[key] = value;
        }
      }

      const group = rule[resource.type];
      if (group === undefined) continue;
      for (const [key, transform] of Object.entries(group)) {
        if (transform === undefined) continue;
        applyTransform(module.specifier, resource, surfaces, key, transform, {
          ...ctx,
          resourceName: resource.name,
        });
      }
    }
  }

  return { name: resource.name, surfaces, tags };
}

function applyTransform(
  specifier: string,
  resource: RequestResource,
  surfaces: Surfaces,
  key: string,
  transform: unknown,
  ctx: GateContext & { resourceName: string },
): void {
  const fields = allowedFields(resource.type, key);
  if (fields === undefined) {
    throw new Error(
      `${specifier}: transform targets ${resource.type}.${key}, which is not a transformable underlying resource (this provider exposes ${Object.keys(surfaceFields[resource.type]).join(", ")})`,
    );
  }

  const args = surfaces[key];
  if (args === undefined) {
    throw new Error(
      `${specifier}: transform targets ${resource.type}.${key}, which this deploy did not render for ${resource.name}`,
    );
  }

  if (typeof transform === "function") {
    const returned = (
      transform as (
        a: Record<string, unknown>,
        c: typeof ctx,
      ) => Record<string, unknown> | void
    )(args, ctx);
    surfaces[key] = returned ?? args;
  } else {
    Object.assign(args, transform);
  }

  validate(specifier, resource.type, key, fields, surfaces[key]!);
}

function validate(
  specifier: string,
  type: SurfaceType,
  key: string,
  fields: readonly string[],
  args: Record<string, unknown>,
): void {
  for (const field of Object.keys(args)) {
    if (!fields.includes(field)) {
      throw new Error(
        `${specifier}: transform sets ${type}.${key}.${field}, which is not a transformable field (this provider exposes ${fields.join(", ")})`,
      );
    }
  }
  for (const field of fields) {
    if (!Object.hasOwn(args, field)) {
      throw new Error(
        `${specifier}: a ${type}.${key} override returned args without ${field} — an override must return the whole args object, or mutate the one it was given`,
      );
    }
  }
}
