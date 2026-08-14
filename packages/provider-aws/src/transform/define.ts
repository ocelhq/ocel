import type {
  AwsSurfaces,
  GateContext,
  SurfaceType,
  TagMap,
  TransformContext,
} from "./surface";

/**
 * Decides whether a rule applies, from ambient context alone. Target a
 * resource by name in an override function instead — a gate cannot see one.
 */
export type Gate = (ctx: GateContext) => boolean;

/** A partial set of args to merge over what the provider defaulted. */
export type Patch<T> = { readonly [K in keyof T]?: T[K] };

/**
 * Receives the fully-defaulted args and either mutates them in place or
 * returns a whole replacement. Returning part of the args fails the deploy.
 */
export type TransformFn<T> = (args: T, ctx: TransformContext) => T | void;

/** Either form a rule may use for one underlying resource. */
export type Transform<T> = Patch<T> | TransformFn<T>;

/**
 * One rule: an optional gate, tags to union into every resource it reaches,
 * and a transform per underlying resource.
 */
export type TransformRule = { readonly if?: Gate; readonly tags?: TagMap } & {
  readonly [T in SurfaceType]?: {
    readonly [K in keyof AwsSurfaces[T]]?: Transform<AwsSurfaces[T][K]>;
  };
};

/** Keys a rule may carry besides the underlying resources it targets. */
export const ruleKeywords = ["if", "tags"] as const;

/**
 * Declares the rules a transform module contributes. Rules apply in the order
 * written, and modules in the order `transforms` lists them, later winning.
 */
export function defineTransform(
  rules: TransformRule | readonly TransformRule[],
): readonly TransformRule[] {
  return Array.isArray(rules)
    ? (rules as readonly TransformRule[])
    : [rules as TransformRule];
}
