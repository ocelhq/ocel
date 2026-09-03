# Examples

Every one of these deploys with `ocel deploy`. Four of them are rungs of one ladder, and
the story of that ladder opens [with-transforms](./with-transforms).

## hello

- [hello-express](./hello-express) — an Express 5 app that declares nothing at all.
- [hello-next](./hello-next) — a Next.js App Router app that declares nothing at all.

## composite

- [express](./express) — todos and documents on Express 5: a postgres database, a blob uploader, a plain variable and a secret.
- [hono](./hono) — the same app on Hono, served on Node.
- [fastify](./fastify) — the same app on Fastify 5.
- [next](./next) — the same app on the Next.js App Router, and the fixture the Next routing and cache suites drive.

## the ladder

- [with-transforms](./with-transforms) — the defaults stop fitting, so a transform module reshapes what ocel provisions.
- [with-sst](./with-sst) — SST owns the database and the network, and ocel deploys the app into it.
- [with-pulumi](./with-pulumi) — the same arrangement in Pulumi.

## workspace

- [workspace](./workspace) — one project serving the `next`, `express` and `hono` apps behind one edge.
