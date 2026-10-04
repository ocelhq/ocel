# realtime/node

An Express 5 app the realtime behavioural suite drives. It declares one realtime resource,
`app`, with `realtime()` from `ocel/realtime`, and serves it at `/api/realtime` with
`createRealtimeHandler` from `ocel/realtime/express`, on its defaults. `authorize` reads the
caller from `Authorization: Bearer user=<id>&orders=<ids>&projects=<ids>&publish=yes`, so each
request says who it is and what it may reach, and a caller loses a channel by sending less.

- `orders/:orderId` admits a caller whose `orders` names the order; only the server publishes.
- `projects/:projectId/deploys/:deployId` is `wildcard`, and admits a caller whose `projects`
  names the project.
- `rooms/:roomId` admits any caller, and its `publish` rule relays a browser publish from a
  caller with `publish=yes`.
- `status` is `public`.

`POST /api/publish` publishes `{ pattern, params, body }` from the server. `POST /api/tokens`
signs `{ header, claims }` with the binding's key, another key or none, so the suite can
replay the shared bad-token vectors against the live transport; it answers only a request
whose `x-journey-nonce` header matches the secret `JOURNEY_NONCE` the app declares, and the
e2e harness sets a fresh one for each run.

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

`ocel destroy` takes the transport and the resource's keys down with the app.
