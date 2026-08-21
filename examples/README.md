# Examples

Four of these examples are three rungs of one ladder, and a team climbs it as it scales.
Every rung deploys with `ocel deploy`; what changes is how much of the provisioning you
have taken back. The other directories here are framework fixtures, not rungs.

**No config — [express](./express).** Ocel provisions everything from what the app
declares. A resource in app code is the provisioning step, so there is nothing to keep in
sync and nothing to configure. Most projects never need to leave this rung.

**Transforms — [with-transforms](./with-transforms).** The defaults stop fitting: a route
needs more memory, production needs a bigger database than a preview, every resource needs
the org's tag. Ocel still provisions all of it; you change how, as reviewable rules in your
repo. This is a smaller step than owning the infrastructure, and it is usually enough.

**Links — [with-sst](./with-sst), [with-pulumi](./with-pulumi).** You need full control of
some infrastructure, so your own IaC tool provisions it and Ocel turns consumer. Which tool
that is decides nothing: the app consuming the infrastructure is the same app either way,
and so is what Ocel asks of you. Ocel never gives away deploying the app itself — that is
the one thing it always provisions, and transforms are how you shape it even here, which is
why a shared-VPC setup needs both rungs at once.

In [Express](./express), [Fastify](./fastify), [Hono](./hono), and
[Next.js](./next), run `ocel run -- pnpm migrate`, then
`ocel dev -- pnpm start`.
