# go

A Go app that declares what the Go SDK offers, each declaration the provisioning step:

- a postgres database, `main`, and a bucket, `uploads`;
- a plain `GREETING` and the secret `JOURNEY_NONCE`;
- the task `exact-echo` and the topic `exact-orders` with its consumer `exact-audit`, all taking
  a `json.RawMessage`, so the handler is handed the payload's JSON text as the worker received
  it. Each handler triggers `sighting` with the kind, name and topic its run context reports
  and the payload as a string, tagged with the run's id or the message's id;
- the realtime resource `app`, served by `Live.Handler()` at `/api/realtime` on its defaults.
  `authorize` reads the caller from
  `Authorization: Bearer user=<id>&orders=<ids>&projects=<ids>&publish=yes`: `orders/:orderId`
  admits a caller whose `orders` names the order, the wildcard
  `projects/:projectId/deploys/:deployId` one whose `projects` names the project,
  `rooms/:roomId` any caller and relays a publish from one with `publish=yes`, and `status` is
  public. `rooms` and `status` take `json.RawMessage` under a JSON Schema of `{ text }`, so a
  server publish can be refused by the schema rather than by Go's types.

`POST /api/wire/trigger` and `POST /api/wire/send` take the request body as the payload,
unparsed, and answer the run's id or the message's id. `GET /api/wire/runs/{id}` answers a
run's payload and output as strings, and `GET /api/wire/sightings/{tag}` what a handler
recorded under that tag.

`POST /api/publish` publishes `{ pattern, params, body }` from the server through the channel
the pattern names. `POST /api/tokens` signs `{ header, claims }` with the binding's key, another
key or none, and answers only a request whose `x-journey-nonce` header matches `JOURNEY_NONCE`.

## Run it

```bash
ocel dev -- go run ./server
```

```bash
ocel deploy
```

On a box, `web`'s image carries the compiled worker, and the default worker runs as a second
container from that image. `ocel destroy` takes it all down again.
