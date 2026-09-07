# go

A Go app serving plain HTTP on `$PORT`, declaring no resources at all, so a journey can ask
whether a go runtime runs on a target at all. The binary knows nothing about the compute it
runs on: serverless, an adapter turns invocations into requests against it; on a container,
the same server answers the proxy directly.

## Run it

```bash
cd server && PORT=3103 go run .
```

```bash
ocel deploy
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.config.ts
```

`ocel destroy` takes it all down again.

`server/probes.go` is the test surface — mounted at `/api/probes`, driven by the suites under
[`tests/journeys`](../../../journeys), and of no use to the product.
