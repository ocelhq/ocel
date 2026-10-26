# with-sst

Rung three of a ladder that starts at [sdk/node](../../sdk/node), whose transforms are rung
two, building on both rungs at once as [with-pulumi](../with-pulumi) does in Pulumi: SST owns
the database and the network it sits in, and ocel still deploys the app — into that network.
Bindings are how the app reaches what SST provisioned; transforms are how ocel renders what it
provisions itself. `sst.config.ts`, `ocel.json` and `transforms/network.transform.ts` are the
three files that make it work.

## Run it

```sh
pnpm install
sst deploy
ocel deploy
```

`sst deploy` also publishes two variables with `bind.variables`: `API_KEY`, the value of an
`sst.Secret` (a placeholder until `sst secret set ApiKey <value>`), and `DATABASE_PORT`, the
database's port, stored as JSON. The app declares both in its discovery folder's `variables.ts`
and reads them as it reads any variable; `ocel env ls` names the SST resource that wrote them, and
`ocel env set` refuses to change one.

That order is a contract, not a convention. Ocel resolves the published records while it
renders, so `ocel deploy` before `sst deploy` — or after `sst remove` — refuses before it
provisions anything, naming the binding it could not read, the property, and the field a
transform was filling with it.

To see it landed, `ocel bindings ls` lists both records and who published them, and

```sh
aws lambda get-function-configuration \
  --function-name "$(aws resourcegroupstaggingapi get-resources \
    --resource-type-filters lambda:function \
    --tag-filters Key=ocel:project,Values=with-sst \
    --query 'ResourceTagMappingList[0].ResourceARN' --output text)" \
  --query VpcConfig
```

reports the subnets and security groups a route's Lambda runs in — the same ids `sst deploy`
published.

`ocel deploy` targets production. Deploy a branch environment with `ocel preview up`, and
`ocel preview rm` to tear it down; publish from SST into the same environment first.
