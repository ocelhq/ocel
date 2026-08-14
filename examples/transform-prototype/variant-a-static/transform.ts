import type { AwsSurfaces, Patch, TagMap } from "../shared/args"
import type { Gate } from "../shared/gate"

export type TransformRule = {
  readonly if?: Gate
  readonly tags?: TagMap
} & {
  readonly [T in keyof AwsSurfaces]?: {
    readonly [R in keyof AwsSurfaces[T]]?: Patch<AwsSurfaces[T][R]>
  }
}

export declare function defineTransform(
  rules: TransformRule | readonly TransformRule[],
): readonly TransformRule[]
