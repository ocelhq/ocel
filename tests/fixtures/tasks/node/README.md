# tasks/node

An Express 5 app the tasks behavioural suite drives. It declares tasks with `task()` from
`ocel/task`, topics and their consumers with `topic()` from `ocel/topic`, and two workers
besides the default one with `worker()` from `ocel/worker`: `ledger`, which serves one of the
two consumers of `orders`, and `capped`, which serves `alpha` and `beta` two runs at a time.
Each task takes one behaviour: retries and aborts (`flaky`), ordering per key (`sequence`),
batches (`tally`), lanes and expiry (`laned`), concurrency (`limited`, `alpha`, `beta`),
`maxDuration` (`outlives`) and `cron` (`heartbeat`). A consumer records what it received by
triggering `receipt`, tagged with the probe it was sent, so the suite reads deliveries back
through `runs.list`. `echo-name` holds a hyphen in its name.

The routes trigger and batch-trigger any task by name, read, list, cancel, replay and
reschedule runs, send to any topic, and list, redrive and purge a consumer's dead letters.

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

```bash
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
```

`ocel destroy` takes the queues, the runs and the workers down with the app.
