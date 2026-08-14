interface ProviderDescriptor {
  package: string
  options: unknown
}

interface AwsProviderOptions {
  region?: string
  transforms?: readonly string[]
}

declare function awsProvider(options?: AwsProviderOptions): ProviderDescriptor

interface OcelConfig {
  slug: string
  provider?: ProviderDescriptor
}

declare function defineConfig(config: OcelConfig): OcelConfig

export default defineConfig({
  slug: "scenario-c",
  provider: awsProvider({
    region: "us-east-1",
    transforms: ["./infra/org-defaults.transform.ts", "./infra/vpc-placement.transform.ts"],
  }),
})
