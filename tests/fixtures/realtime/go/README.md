# realtime/go

The Go app the realtime behavioural suite drives, declaring what `realtime/node` declares with
the Go SDK: the realtime resource `app` from `ocel.Realtime`, its channels from `ocel.Channel`,
and the handler `Live.Handler()` mounted at `/api/realtime` on its defaults. `authorize` reads
the caller from `Authorization: Bearer user=<id>&orders=<ids>&projects=<ids>&publish=yes`.

- `orders/:orderId` admits a caller whose `orders` names the order; only the server publishes.
- `projects/:projectId/deploys/:deployId` is wildcard, and admits a caller whose `projects`
  names the project.
- `rooms/:roomId` admits any caller, and its publish rule relays a browser publish from a caller
  with `publish=yes`. Its events are `json.RawMessage` under a JSON Schema of `{ text }`, so a
  server publish can be refused by the schema rather than by Go's types.
- `status` is public, under the same schema.

`POST /api/publish` publishes `{ pattern, params, body }` from the server through the channel
the pattern names. `POST /api/tokens` signs `{ header, claims }` with the binding's key, another
key or none, and answers only a request whose `x-journey-nonce` header matches the secret
`JOURNEY_NONCE` the app declares.

## Run it

```bash
ocel dev -- go run ./server
```

`ocel destroy` takes the transport and the resource's keys down with the app.
