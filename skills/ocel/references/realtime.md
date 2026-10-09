# Realtime

Live updates from the server to browsers over WebSockets. A realtime resource declares:
- **channels:** patterns with `:params`, each with an event schema;
- **who may subscribe:** public, or a rule;
- **who may publish:** server only, or a rule.

An `authorize` function turns the incoming request into the caller's auth. The server publishes; browsers subscribe with a short-lived token the app hands out.

In `ocel dev`, the realtime gateway runs locally.

## Declare and publish

```ts
import { realtime } from "ocel/realtime"; // handlers: ocel/realtime/next | /express | /hono

export const live = realtime("app", {
  authorize: async (req) => readSession(req),
  channels: {
    "orders/:orderId": {
      schema: z.object({ status: z.string() }),
      subscribe: ({ auth, params }) => auth.orderIds.includes(params.orderId),
    },
    status: { schema: Note, subscribe: "public" },
  },
});

await live.publish("orders/:orderId", { params: { orderId }, body: { status: "paid" } });
// browser: ocel/realtime/client
```

```go
var Live = ocel.Realtime("app", ocel.RealtimeAuthorize(func(r *http.Request) (*Caller, error) { /* … */ }))
var Orders = ocel.Channel[OrderEvent, OrderParams](Live, "orders/:orderId",
    ocel.ChannelSubscribe(func(c *ocel.SubscribeContext[Caller, OrderParams]) (bool, error) { /* … */ }))
// OrderParams fields carry `realtime:"orderId"` tags; ocel.ChannelSubscribePublic() for public channels
```

```python
live = ocel.realtime("app", authorize=read_caller)

@live.channel("orders/:orderId", schema=OrderEvent)
def orders(ctx: ocel.RuleContext) -> bool: ...

orders.publish(event, orderId="1")
app = live.asgi()  # the token endpoint to mount; live.wsgi() for WSGI apps
```

```rust
#[derive(ocel::Channel)]
#[ocel(realtime = "app", pattern = "orders/:order_id", event = OrderEvent)]
struct Orders { order_id: String }

let rt = ocel::realtime::Realtime::builder("app").authorize(read_caller).subscribe::<Orders>(rule).build()?;
rt.publish(&Orders { order_id }, &event).await?;
// requires the `realtime` feature; with `axum` too, ocel::realtime::axum::router(rt) serves the token endpoint
```

## Limits

- **Patterns:** at most 4 segments.
- **Event size:** at most 240 KiB.
- **Tokens:** live 10–300 s (`tokenTtl`, default 60 s).

The SDK's own types and docstrings are the full API.
