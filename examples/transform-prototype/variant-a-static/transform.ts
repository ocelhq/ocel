import type { AwsSurfaces, Patch, TagMap } from "../shared/args"

export type StaticTransform = {
  readonly tags?: TagMap
} & {
  readonly [T in keyof AwsSurfaces]?: {
    readonly [R in keyof AwsSurfaces[T]]?: Patch<AwsSurfaces[T][R]>
  }
}

export declare function defineTransform(transform: StaticTransform): StaticTransform
