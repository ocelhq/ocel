import type { AwsSurfaces, Patch, TagMap } from "../shared/args"
import type { EnvClass, Selector } from "../shared/selector"

export interface TransformContext {
  readonly resourceName: string
  readonly app: string | undefined
  readonly envClass: EnvClass
  readonly env: string
}

export type TransformFn<T> = (args: T, ctx: TransformContext) => T | void

export type Transform<T> = Patch<T> | TransformFn<T>

export type TransformRule = {
  readonly when?: Selector
  readonly tags?: TagMap
} & {
  readonly [T in keyof AwsSurfaces]?: {
    readonly [R in keyof AwsSurfaces[T]]?: Transform<AwsSurfaces[T][R]>
  }
}

export declare function defineTransform(
  rules: TransformRule | readonly TransformRule[],
): readonly TransformRule[]
