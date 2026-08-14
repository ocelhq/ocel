interface ProviderDescriptor {
  package: string
  options: unknown
}

interface OcelConfig {
  slug: string
  provider?: ProviderDescriptor
  transforms?: readonly string[]
}

declare function defineConfig(config: OcelConfig): OcelConfig

export default defineConfig({
  slug: "scenario-c",
  provider: { package: "@ocel/provider-aws", options: { region: "us-east-1" } },
  transforms: ["./infra/org-defaults.transform.ts", "./infra/vpc-placement.transform.ts"],
})
