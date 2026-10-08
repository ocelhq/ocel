# Topic

Publish/subscribe messaging. Code sends a message to a topic, and every **consumer** declared on it gets its own copy. Each consumer retries on its own, and messages that keep failing go to a dead-letter list.
- Use a task when one function should run per trigger.
- Use a topic when one event should set off several independent reactions.

In `ocel dev`, topics run on the local queue engine.

## Declare, consume, send

```ts
import { topic } from "ocel/topic";

export const orders = topic<Order>("orders");
orders.consumer("send-receipt", async (order) => { /* … */ }, { retry: { maxAttempts: 5 } });
orders.consumer("audit-log", async (order) => { /* … */ });

await orders.send(order, { delay: "5s", idempotencyKey: order.id });
```

```go
var Orders = ocel.Topic[Order]("orders")
var Receipts = Orders.Consumer("send-receipt", func(ctx context.Context, o Order) error { /* … */ })

id, err := infra.Orders.Send(ctx, order)
```

```python
orders = ocel.topic("orders", schema=Order)

@orders.consumer("send-receipt", retry=ocel.Retry(max_attempts=5))
def send_receipt(order: Order, ctx): ...

orders.send(order, idempotency_key=order.id)
```

```rust
#[ocel(retry(max_attempts = 5))]
pub orders: ocel::Topic<Order>, // generates Infra::ORDERS

#[ocel::consumer(topic = Infra::ORDERS)]
async fn send_receipt(order: Order, run: &ocel::Run) -> Result<(), ocel::RunError> { /* … */ }

infra.orders.send(order).await?;
```

## Behaviour

- **Ordering:** on an `ordered` topic, messages sent with the same `key` are delivered one at a time, in order.
- **Batches:** `batchConsumer` (Go `BatchConsumer`, Rust `#[ocel::batch_consumer]`) receives messages in batches.
- **Failures:**
  - A consumer's error retries that consumer only.
  - Messages that use up their retries go to that consumer's dead-letter list, which can be read, redriven or purged (`deadLetter(name)` in TS).
- **Workers:** consumers run on the default worker unless `worker` names another (see `task.md`).

The SDK's own types and docstrings are the full API.
