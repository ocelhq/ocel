# node

An Express 5 app on Node that declares every primitive the SDK offers, each one the
provisioning step:

- a postgres database, `main`, holding todos and documents;
- a bucket named `uploads` whose `document` uploader takes images and PDFs under `documents/`
  and writes a row when an upload completes;
- a plain `GREETING`, and the secrets `SECRET_TOKEN` and `JOURNEY_NONCE`;
- three kv stores: `cache`, holding an entry of every shape, a json entry that reads an invalid
  value as a miss, an entry with a ttl, and two entries one `/` apart; `bounded`, which evicts
  nothing; and `evicting`, which evicts by lru;
- tasks, topics with their consumers, and two workers besides the default one: `ledger`, which
  serves one of the two consumers of `orders`, and `capped`, which serves `alpha` and `beta` two
  runs at a time. Each task takes one behaviour: retries and aborts (`flaky`), ordering per key
  (`sequence`), batches (`tally`), lanes and expiry (`laned`), concurrency (`limited`, `alpha`,
  `beta`), cancellation (`ticking`), `maxDuration` (`outlives`) and `cron` (`heartbeat`). A
  consumer records what it received by triggering `receipt`, tagged with the probe it was sent;
- one realtime resource, `app`, served at `/api/realtime` on its defaults. `authorize` reads the
  caller from `Authorization: Bearer user=<id>&orders=<ids>&projects=<ids>&publish=yes`:
  `orders/:orderId` admits a caller whose `orders` names the order, the wildcard
  `projects/:projectId/deploys/:deployId` one whose `projects` names the project,
  `rooms/:roomId` any caller and relays a publish from one with `publish=yes`, and `status` is
  `public`.

`transforms/defaults.transform.ts` reshapes what Ocel provisions for it: more memory for a
function on aws, a bigger Aurora cluster in production, postgres arguments on a box, and a
cost-center tag on everything.

`wire.ts`, beside the other declarations, is what the wire checks read, as in `sdk/go`: the
task `verbatim` and the topic `notices` with its consumer `notice-log`, whose handlers trigger
`sighting` with the kind, name and topic their run context reports and the payload's JSON text.
Its routes under `/api/wire` hold no hyphen, since the app runs through `pnpm dev` (#1526).

`/api/kv/password-report` and `POST /api/tokens` answer only a request whose `x-journey-nonce`
header matches `JOURNEY_NONCE`, which the e2e harness sets fresh for each run.

## Run it

```bash
pnpm install
ocel run -- pnpm migrate
ocel dev
```

```bash
ocel deploy
```

On a box, `web`'s image carries the bundled worker entry, and `worker`, `ledger` and `capped`
each run as a container of their own from that image. `ocel projects destroy` takes it all down again.

`src/probes.ts` is the test surface — mounted at `/api/probes`, driven by the suites under
[`tests/e2e`](../../../e2e), and of no use to the product.
