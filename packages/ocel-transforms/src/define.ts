import type { AwsSurfaces } from "./aws";
import type { TransformBindings } from "./index";

/** The environment classes a deploy can target. */
export type EnvClass = "development" | "preview" | "production";

/**
 * What a rule's `if` gate is allowed to decide on: the environment being
 * deployed and the app a candidate resource belongs to. Resources shared
 * across apps carry no `app`, so `ctx.app === "api"` is false for them.
 */
export interface GateContext {
  readonly envClass: EnvClass;
  readonly env: string;
  readonly app: string | undefined;
}

/** Decides whether a rule applies, from ambient context alone. */
export type Gate = (ctx: GateContext) => boolean;

/**
 * Tags a rule unions into every taggable resource it reaches. Keys under the
 * `ocel:` prefix are ocel's own and are rejected at deploy.
 */
export type TagMap = Record<string, string>;

/** The prefix ocel reserves for the tags it writes itself. */
export const reservedTagPrefix = "ocel:";

/** What a provider renders that a transform may patch, keyed by provider name. */
export interface ProviderSurfaces {
  aws: AwsSurfaces;
}

/** The providers that render patchable resources. */
export type ProviderName = keyof ProviderSurfaces;

/**
 * One rule: an optional gate, tags to union into every resource it reaches,
 * and a patch per underlying resource, under the provider that renders it.
 */
export type TransformRule = { readonly if?: Gate; readonly tags?: TagMap } & {
  readonly [P in ProviderName]?: {
    readonly [T in keyof ProviderSurfaces[P]]?: {
      readonly [K in keyof ProviderSurfaces[P][T]]?: ProviderSurfaces[P][T][K];
    };
  };
};

/** Keys a rule may carry besides the providers it targets. */
export const ruleKeywords = ["if", "tags"] as const;

/** What a callback form of `defineTransform` is handed, and nothing besides. */
export interface TransformInputs {
  readonly bindings: TransformBindings;
  readonly envClass: EnvClass;
  readonly env: string;
}

/** The rules a module contributes, written down or returned from the callback. */
export type TransformRules = TransformRule | readonly TransformRule[];

/** A module's default export: the rules it contributes, once the deploy asks. */
export interface TransformDefinition {
  readonly rules: (inputs: TransformInputs) => readonly TransformRule[];
}

/**
 * Declares the rules a transform module contributes. Rules apply in the order
 * written, and modules in the order `transforms` lists them, later winning.
 *
 * The callback form is handed the environment being deployed and `bindings`,
 * the placeholders for the records bound to this project:
 * `bindings.custom.network.subnetIds` is filled by the deploy, so the rules
 * stay data a reviewer can read.
 */
export function defineTransform(
  rules: TransformRules | ((inputs: TransformInputs) => TransformRules),
): TransformDefinition {
  return {
    rules: (inputs) => {
      const authored = typeof rules === "function" ? rules(inputs) : rules;
      return Array.isArray(authored)
        ? (authored as readonly TransformRule[])
        : [authored as TransformRule];
    },
  };
}

/** Whether a module's default export came from `defineTransform`. */
export function isTransformDefinition(value: unknown): value is TransformDefinition {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { rules?: unknown }).rules === "function"
  );
}
