export type EnvClass = "development" | "preview" | "production"

export interface GateContext {
  readonly envClass: EnvClass
  readonly env: string
  readonly app: string | undefined
}

export type Gate = (ctx: GateContext) => boolean
