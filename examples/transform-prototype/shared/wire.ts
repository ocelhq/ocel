export interface WireLinkOutput {
  readonly $link: { readonly link: string; readonly property: string }
}

export type WireValue =
  | string
  | number
  | boolean
  | WireLinkOutput
  | readonly WireValue[]
  | { readonly [key: string]: WireValue }

export type WireSurfacePatch = Record<string, WireValue>

export interface StaticTransformWire {
  readonly transforms: readonly {
    readonly module: string
    readonly spec: {
      readonly [typeKey: string]: { readonly [resourceKey: string]: WireSurfacePatch }
    }
    readonly tags: Record<string, string>
  }[]
}

export interface EvaluateRequest {
  readonly modules: readonly string[]
  readonly resources: readonly {
    readonly type: "function" | "bucket" | "postgres"
    readonly name: string
    readonly surfaces: Record<string, WireSurfacePatch>
  }[]
}

export interface EvaluateResponse {
  readonly resources: readonly {
    readonly name: string
    readonly surfaces: Record<string, WireSurfacePatch>
  }[]
}
