# Examples

Every one of these deploys with `ocel deploy`. Three of them are rungs of a ladder that
starts at [express](./express), and the story of that ladder opens
[with-transforms](./with-transforms).

An app that declares nothing is not an example of its own: the journey harness runs one of
these with its resources suppressed, and the app must still boot and serve.

## composite

- [express](./express) — todos and documents on Express 5: a postgres database, a blob uploader, a plain variable and a secret.
- [hono](./hono) — the same app on Hono, served on Node.
- [fastify](./fastify) — the same app on Fastify 5.
- [next](./next) — the same app on the Next.js App Router, and the fixture the Next routing and cache suites drive.

## with-transforms

- [with-transforms](./with-transforms) — the defaults stop fitting, so a transform module reshapes what ocel provisions.

## with-sst

- [with-sst](./with-sst) — SST owns the database and the network, and ocel deploys the app into it.

## with-pulumi

- [with-pulumi](./with-pulumi) — the same arrangement in Pulumi.

## workspace

- [workspace](./workspace) — one project serving a Next.js app and an Express app behind one edge, declaring its postgres, bucket and env once for both.
