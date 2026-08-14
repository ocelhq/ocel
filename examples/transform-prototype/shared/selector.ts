export type EnvClass = "development" | "preview" | "production"

export interface Selector {
  readonly app?: string | readonly string[]
  readonly name?: string | readonly string[]
  readonly envClass?: EnvClass | readonly EnvClass[]
  readonly env?: string | readonly string[]
}
