# Task

A background job: a function Ocel runs on a worker when triggered. It comes with retries, concurrency limits, timeouts and an optional `cron` schedule.

Each trigger is a **run**, which can be listed, retrieved, cancelled and replayed. In `ocel dev`, tasks run on the local queue engine and on the worker processes `ocel dev` starts.

## Declare and trigger

```ts
import { task, runs, AbortTaskRunError } from "ocel/task";

export const sendReceipt = task("send-receipt", {
  retry: { maxAttempts: 3, minDelay: "1s", maxDelay: "30s" },
  run: async (payload: { orderId: string }, { ctx, signal }) => { /* … */ },
});
export const nightly = task("nightly-report", { cron: "0 3 * * *", run: async () => { /* … */ } });

await sendReceipt.trigger({ orderId }, { delay: "10s", tags: ["checkout"] });
const page = await runs.list({ task: "send-receipt", limit: 20 });
```

```go
var SendReceipt = ocel.Task("send-receipt", func(ctx context.Context, p Receipt) (Result, error) { /* … */ })
var Nightly = ocel.Task("nightly-report", report, ocel.Cron("0 3 * * *"))

handle, err := infra.SendReceipt.Trigger(ctx, Receipt{OrderID: id}, ocel.Tags("checkout"))
run, err := ocel.RetrieveRun(ctx, handle.ID)
```

```python
@ocel.task("send-receipt", retry=ocel.Retry(max_attempts=3))
def send_receipt(payload: dict, ctx: ocel.RunContext): ...

@ocel.task("nightly-report", cron="0 3 * * *")
def nightly(payload: dict, ctx: ocel.RunContext): ...

send_receipt.trigger({"order_id": order_id}, delay=10, tags=["checkout"])  # or trigger_async
```

```rust
#[ocel::task(cron = "0 3 * * *", retry(max_attempts = 5))]
async fn nightly_report(_: (), run: &ocel::Run) -> Result<(), ocel::RunError> { /* … */ }

let run = send_receipt.trigger(receipt).delay(Duration::from_secs(10)).await?;
```

Rust needs `#[ocel::main]` on `main`. A Rust task's name defaults to the function name, with `_` written as `-`.

## Behaviour

- **Failure:**
  - Throwing or returning an error fails the attempt; `retry` decides whether another attempt follows.
  - `AbortTaskRunError` (TS, Python) or `ocel.ErrAbort` (Go) ends the run with no retry.
- **Cron:** a standard five-field expression.
- **Trigger options:**
  - `delay`, `ttl`, `idempotencyKey`, `debounce`, `tags`, `metadata`.
  - `key` on an `ordered` task: runs that share a key go one at a time.
- **Workers:**
  - Tasks run on the default worker unless `worker` names another.
  - A named worker (`worker("media", { concurrency })`) runs as the `ocel.json` app of the same name.
  - Declare one only to isolate heavy work.

The SDK's own types and docstrings are the full API.
