import type { AwsSurfaces, Patch, TagMap } from "../shared/args"

export interface TransformContext {
  readonly resourceName: string
}

export type TransformFn<T> = (args: T, ctx: TransformContext) => T | void

export type Transform<T> = Patch<T> | TransformFn<T>

export type AwsTransform = {
  readonly tags?: TagMap
} & {
  readonly [T in keyof AwsSurfaces]?: {
    readonly [R in keyof AwsSurfaces[T]]?: Transform<AwsSurfaces[T][R]>
  }
}

export declare function defineTransform(transform: AwsTransform): AwsTransform
