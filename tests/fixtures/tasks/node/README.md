# tasks/node

An Express 5 app the tasks behavioural suite drives. It declares tasks with `task()` from
`ocel/task`, topics and their consumers with `topic()` from `ocel/topic`, and two workers
besides the default one with `worker()` from `ocel/worker`: `ledger`, which serves one of the
two consumers of `orders`, and `capped`, which serves `alpha` and `beta` two runs at a time.
Each task takes one behaviour: retries and aborts (`flaky`), ordering per key (`sequence`),
batches (`tally`), lanes and expiry (`laned`), concurrency (`limited`, `alpha`, `beta`),
cancellation (`ticking`), `maxDuration` (`outlives`) and `cron` (`heartbeat`). A consumer
records what it received by triggering `receipt`, tagged with the probe it was sent, so the
suite reads deliveries back through `runs.list`; `tally` records each batch it is delivered
and `ticking` each tick until its run is aborted the same way. `echo-name` holds a hyphen in its name.

The routes trigger and batch-trigger any task by name, read, list, cancel, replay and
reschedule runs, send to any topic, and list, redrive and purge a consumer's dead letters.

`wire.ts`, beside the other declarations, is what the wire checks read, as in `tasks/go`: the
task `verbatim` and the topic `notices` with its consumer `notice-log`, whose handlers trigger
`sighting` with the kind, name and topic their run context reports and the payload's JSON
text, and `verbatim` returns that text as its output. The routes under `/api/wire` send the
request body as the payload's JSON text, and answer the run's or message's id, a run's payload
and output as JSON text, and what a handler recorded under a tag. Their names hold no hyphen,
since the app runs through `pnpm dev` (#1526).

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

```bash
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
```

On a box, `web`'s image carries the bundled worker entry, and `worker`, `ledger` and `capped`
each run as a container of their own from that image, which the box's agent delivers to.

`ocel destroy` takes the queues, the runs and the workers down with the app.
