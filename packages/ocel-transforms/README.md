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

export default defineTransform(({ bindings, envClass }) => ({
  aws: {
    function: {
      lambda: {
        memorySize: envClass === "production" ? 2048 : 512,
        vpcConfig: { subnetIds: bindings.custom.network.subnetIds },
      },
    },
  },
}));
```

The keys under `aws` are the ocel resources; the keys under those are the
Pulumi resources the provider constructs for them, typed from `@pulumi/aws`.

`bindings.<type>.<name>.<property>` reads a record your own infrastructure
published — `<type>.<name>` is the key your `ocel.json` binds under, and
`custom.<name>` reads a record nothing declared. Run `ocel bindings generate`
to write those names and their types down.

## License

MIT
