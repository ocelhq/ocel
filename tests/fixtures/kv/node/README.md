# kv/node

An Express 5 app the kv behavioural suite drives. It declares three kv stores with `kv()` from
`ocel/kv`: `cache`, holding an entry of every shape, a json entry that reads an invalid value
as a miss, an entry with a ttl, and two entries one `/` apart; `bounded`, which evicts nothing;
and `evicting`, which evicts by lru. Each route reaches one of them through its typed entries
or its native client. The declarations sit in the default discovery directory and are the
provisioning step.

`/api/kv/password-report` answers with the `cache` password in clear, so it answers only a
request whose `x-password-report-nonce` header matches the secret `PASSWORD_REPORT_NONCE` the
app declares; the journey sets a fresh one for each run.

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

```bash
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
```

`ocel destroy` takes the stores, their data and their passwords down with the app.
