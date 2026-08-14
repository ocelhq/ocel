import type { AwsSurfaces, Patch, TagMap } from "../shared/args"
import type { Gate, GateContext } from "../shared/gate"

export interface TransformContext extends GateContext {
  readonly resourceName: string
}

export type TransformFn<T> = (args: T, ctx: TransformContext) => T | void

export type Transform<T> = Patch<T> | TransformFn<T>

export type TransformRule = {
  readonly if?: Gate
  readonly tags?: TagMap
} & {
  readonly [T in keyof AwsSurfaces]?: {
    readonly [R in keyof AwsSurfaces[T]]?: Transform<AwsSurfaces[T][R]>
  }
}

export declare function defineTransform(
  rules: TransformRule | readonly TransformRule[],
): readonly TransformRule[]
