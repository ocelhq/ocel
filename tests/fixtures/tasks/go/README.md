# tasks/go

A Go app the tasks suite drives for what crosses the wire. It declares the task `exact-echo`
and the topic `exact-orders` with its consumer `exact-audit`, all taking a `json.RawMessage`,
so the handler is handed the payload's JSON text as the worker received it. Each handler
triggers `sighting` with the kind, name and topic its run context reports and the payload as
a string, tagged with the run's id or the message's id, and `exact-echo` answers its payload
as its output.

`POST /api/wire/trigger` and `POST /api/wire/send` take the request body as the payload,
unparsed, and answer the run's id or the message's id. `GET /api/wire/runs/{id}` answers a run's
payload and output as strings, and `GET /api/wire/sightings/{tag}` what a handler recorded
under that tag.

## Run it

```bash
ocel dev -- go run ./server
```

`ocel destroy` takes the queues, the runs and the worker down with the app.
