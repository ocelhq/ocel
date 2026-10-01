# tasks/go

A Go app the tasks suite drives for what crosses the wire. It declares the task `exact-echo`
and the topic `exact-orders` with its consumer `exact-audit`, all taking a `json.RawMessage`,
so the handler is handed the payload's JSON text as the worker received it. Each handler
triggers `seen` with the kind, name and topic its run context reports and the payload as a
string, tagged with the run's id or the message's id, and `exact-echo` answers its payload
as its output.

`POST /api/tasks/exact-echo/trigger` and `POST /api/topics/exact-orders/send` take the
request body as the payload, unparsed. `GET /api/runs/{id}` answers a run's payload and
output as strings, and `GET /api/seen/{tag}` what a handler recorded under that tag.

## Run it

```bash
ocel dev -- go run ./server
```

`ocel destroy` takes the queues, the runs and the worker down with the app.
