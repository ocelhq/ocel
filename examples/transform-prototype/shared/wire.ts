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

export interface WireSelector {
  readonly app?: readonly string[]
  readonly name?: readonly string[]
  readonly envClass?: readonly string[]
  readonly env?: readonly string[]
}

export interface WireRule {
  readonly when?: WireSelector
  readonly tags?: Record<string, string>
  readonly spec: {
    readonly [typeKey: string]: { readonly [resourceKey: string]: WireSurfacePatch }
  }
}

export interface StaticTransformWire {
  readonly transforms: readonly {
    readonly module: string
    readonly rules: readonly WireRule[]
  }[]
}

export interface EvaluateRequest {
  readonly modules: readonly string[]
  readonly envClass: string
  readonly env: string
  readonly resources: readonly {
    readonly type: "function" | "bucket" | "postgres"
    readonly name: string
    readonly app?: string
    readonly surfaces: Record<string, WireSurfacePatch>
  }[]
}

export interface EvaluateResponse {
  readonly resources: readonly {
    readonly name: string
    readonly surfaces: Record<string, WireSurfacePatch>
  }[]
}
