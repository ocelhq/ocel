# kv/node

An Express 5 app the kv behavioural suite drives. It declares three kv stores with `kv()` from
`ocel/kv`: `cache`, holding an entry of every shape, a json entry that reads an invalid value
as a miss, an entry with a ttl, and two entries one `/` apart; `bounded`, which evicts nothing;
and `evicting`, which evicts by lru. Each route reaches one of them through its typed entries
or its native client. The declarations sit in the default discovery directory and are the
provisioning step.

`/api/kv/password-report` answers with the `cache` password in clear, so it answers only a
request whose `x-journey-nonce` header matches the secret `JOURNEY_NONCE` the app declares;
the e2e harness sets a fresh one for each run.

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

```bash
ocel deploy
```

`ocel destroy` takes the stores, their data and their passwords down with the app.
