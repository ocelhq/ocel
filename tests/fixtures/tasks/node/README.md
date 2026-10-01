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

`wire.ts`, beside the other declarations, is what the wire checks read, as in `tasks/go`:
the task `verbatim` and the topic `notices` with its consumer `notice-log`, whose bindings
are rewritten to name `physical-verbatim` and `physical-notices`; a proxy in front of the
runtime address that records each `Trigger` and `Send` it passes on; and, in a worker, a
proxy on the worker's port that records each envelope as an `envelope` run before passing it
on. The routes under `/api/wire` take the request body as the payload, parsing it with each
number kept as its source text, and answer the requests the SDK sent, the names, a run's
payload and output as JSON text, and what was recorded under a tag. Their names hold no
hyphen, since the app runs through `pnpm dev` (#1526).

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

```bash
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
```

`ocel destroy` takes the queues, the runs and the workers down with the app.
