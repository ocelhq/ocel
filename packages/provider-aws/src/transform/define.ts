import type {
  AwsSurfaces,
  GateContext,
  SurfaceType,
  TransformContext,
} from "./surface";

export type Gate = (ctx: GateContext) => boolean;

export type Patch<T> = { readonly [K in keyof T]?: T[K] };

export type TransformFn<T> = (args: T, ctx: TransformContext) => T | void;

export type Transform<T> = Patch<T> | TransformFn<T>;

export type TransformRule = { readonly if?: Gate } & {
  readonly [T in SurfaceType]?: {
    readonly [K in keyof AwsSurfaces[T]]?: Transform<AwsSurfaces[T][K]>;
  };
};

export function defineTransform(
  rules: TransformRule | readonly TransformRule[],
): readonly TransformRule[] {
  return Array.isArray(rules) ? (rules as readonly TransformRule[]) : [rules as TransformRule];
}
