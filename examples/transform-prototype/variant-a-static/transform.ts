import type { AwsSurfaces, Patch, TagMap } from "../shared/args"
import type { Selector } from "../shared/selector"

export type TransformRule = {
  readonly when?: Selector
  readonly tags?: TagMap
} & {
  readonly [T in keyof AwsSurfaces]?: {
    readonly [R in keyof AwsSurfaces[T]]?: Patch<AwsSurfaces[T][R]>
  }
}

export declare function defineTransform(
  rules: TransformRule | readonly TransformRule[],
): readonly TransformRule[]
