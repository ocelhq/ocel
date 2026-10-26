# @ocel/transforms

Patch the underlying cloud resources ocel provisions, from a module ocel runs
before it provisions anything.

## Install

```bash
pnpm add -D @ocel/transforms
```

## Use

Write a module, and list it in `ocel.json`:

```json
{
  "transforms": ["./transforms/network.transform.ts"]
}
```

```ts
import { defineTransform } from "@ocel/transforms";

export default defineTransform(({ bindings, envTier }) => ({
  aws: {
    function: {
      lambda: {
        memorySize: envTier === "production" ? 2048 : 512,
        vpcConfig: { subnetIds: bindings.custom.network.subnetIds },
      },
    },
  },
}));
```

The keys under `aws` are the ocel resources; the keys under those are the
Pulumi resources the provider constructs for them, typed from `@pulumi/aws`.

Under `gcp`, each field is named as `@pulumi/gcp` names it in the args of the
same resource. The Google Cloud provider calls Google's APIs itself for most of
what it makes, so those resources take only the paths it translates, and a
path off the list is refused where the rule is written:

```ts
export default defineTransform({
  gcp: {
    postgres: { instance: { settings: { tier: "db-custom-2-7680" } } },
    function: {
      service: { template: { containers: [{ resources: { cpuIdle: false } }] } },
    },
  },
});
```

See [Transforms](https://ocel.dev/docs/transforms) for what each provider takes.

`bindings.<type>.<name>.<property>` reads a record your own infrastructure
published — `<type>.<name>` is the key your `ocel.json` binds under, and
`custom.<name>` reads a record nothing declared. Run `ocel bindings generate`
to write those names and their types down.

## License

Apache-2.0
