# tasks/go

A Go app the tasks suite drives for what crosses the wire. It declares the task `exact-echo`
and the topic `exact-orders` with its consumer `exact-audit`, all taking a `json.RawMessage`,
so the handler is handed the payload's JSON text as the worker received it. Each handler
triggers `sighting` with the kind, name and topic its run context reports and the payload as
a string, tagged with the run's id or the message's id, and `exact-echo` answers its payload
as its output.

Three things in `infra` run in every process before the SDK is used, so the suite can see the
wire rather than what the SDK makes of it:

- the bindings of `exact-echo` and `exact-orders` are rewritten to name `physical-exact-echo`
  and `physical-exact-orders`, so the name a binding holds differs from the declared one, as
  it does on a target that provisions under its own names;
- the runtime address is pointed at a proxy that records the name and payload of every
  `Trigger` and `Send` it passes on;
- in a worker, a proxy takes the port the worker was given and passes each envelope on to
  the worker behind it, first triggering `envelope` with the envelope's text, tagged with its
  execution and message ids.

`POST /api/wire/trigger` and `POST /api/wire/send` take the request body as the payload,
unparsed, and answer the id with the requests the SDK sent. `GET /api/wire/names` answers the
declared and bound names, `GET /api/wire/runs/{id}` a run's payload and output as strings,
`GET /api/wire/sightings/{tag}` what a handler recorded under that tag, and
`GET /api/wire/envelopes/{tag}` the envelopes recorded under it.

## Run it

```bash
ocel dev -- go run ./server
```

`ocel destroy` takes the queues, the runs and the worker down with the app.
