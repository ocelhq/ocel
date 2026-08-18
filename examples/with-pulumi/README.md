# with-pulumi

Rung three of the [examples ladder](../README.md), in Pulumi rather than
[SST](../with-sst), and standing on the same two rungs: Pulumi owns the database and the
network it sits in, and ocel still deploys the app — into that network. Links are how the
app reaches what Pulumi provisioned; transforms are how ocel renders what it provisions
itself. `index.ts`, `ocel.config.ts` and `infra/network.transform.ts` are the three files
that carry it.

## Run it

```sh
pnpm install
pulumi config set aws:region us-east-1
pulumi config set --secret dbPassword "$(openssl rand -hex 16)"
pulumi up
ocel deploy
```

That order is a contract, not a convention. Ocel resolves the published records while it
renders, so `ocel deploy` before `pulumi up` — or after `pulumi destroy` — refuses before it
provisions anything, naming the link it could not read, the property, and the field a
transform was filling with it.

To see it landed, `ocel link ls` lists both records and who published them, and

```sh
aws lambda get-function-configuration \
  --function-name "$(aws resourcegroupstaggingapi get-resources \
    --resource-type-filters lambda:function \
    --tag-filters Key=ocel:project,Values=with-pulumi \
    --query 'ResourceTagMappingList[0].ResourceARN' --output text)" \
  --query VpcConfig
```

reports the subnets and security groups a route's Lambda runs in — the same ids `pulumi up`
published.

`ocel deploy` targets production. Stand a branch environment up with `ocel preview up`, and
`ocel preview rm` to tear it down; publish from Pulumi into the same environment first.
