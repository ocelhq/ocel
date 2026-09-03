# with-transforms

Four of the [examples](../README.md) are three rungs of one ladder, and a team climbs it as
it scales. Every rung deploys with `ocel deploy`; what changes is how much of the
provisioning you have taken back.

**No config — [express](../express).** The reference composite: a provider-less base config
names the slug and the app, and `ocel.aws.config.ts` and `ocel.vps.config.ts` import it and
add a provider each. Everything else Ocel provisions from what the app declares. A resource
in app code is the provisioning step, so there is nothing to keep in sync and nothing to
configure. Most projects never need to leave this rung.

**Transforms — this example.** The defaults stop fitting: a route needs more memory,
production needs a bigger database than a preview, every resource needs the org's tag. Ocel
still provisions all of it; you change how, as reviewable rules in your repo. This is a
smaller step than owning the infrastructure, and it is usually enough.

**Links — [with-sst](../with-sst), [with-pulumi](../with-pulumi).** You need full control of
some infrastructure, so your own IaC tool provisions it and Ocel turns consumer. Which tool
that is decides nothing: the app consuming the infrastructure is the same app either way,
and so is what Ocel asks of you. Ocel never gives away deploying the app itself — that is
the one thing it always provisions, and transforms are how you shape it even here, which is
why a shared-VPC setup needs both rungs at once.

The app on this rung is modeled on the [express](../express) example, with a `postgres`
resource; what it adds is `ocel.config.ts` and `infra/defaults.transform.ts`. The module is
the whole of what it has to show.

## Run it

```sh
pnpm install
ocel deploy
```

To see the effect, look at what landed in your AWS account: the Lambda behind each route,
the Aurora cluster behind `main`, and the tags on both.

`ocel deploy` targets production. Stand a branch environment up with `ocel preview up` to
see the same module render a different result, and `ocel preview rm` to tear it down.
