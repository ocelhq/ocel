# KV

A key-value store: one Valkey instance per declaration. It suits caches, counters, sessions, rate limits, lists and sets. In `ocel dev` it runs in Docker.

A store declares typed **entries**. Each entry is a key pattern with `:params` and a shape:
- `text`
- `counter`
- `json` (validated against a schema)
- `list`
- `set`

Patterns within one store must not overlap; overlapping patterns are refused at declaration. The raw client is always there for anything the entries don't cover.

## Declare and use

```ts
import { kv } from "ocel/kv";

export const cache = kv("cache", {
  memory: "256mb",
  eviction: "allkeys-lru",
  entries: {
    visits: kv.counter("visits/:page"),
    profile: kv.json("profile/:userId", { schema: Profile }),
    session: kv.text("session/:id", { ttl: "30m" }),
  },
});

await cache.visits.increment({ page: "home" });
const profile = await cache.profile.get({ userId });
// cache.client is an ioredis client; cache.connectionString for other clients
```

```go
var Cache = ocel.KV("cache", ocel.KVEviction("allkeys-lru"), ocel.KVMemory("256mb"))
var Visits = ocel.KVCounter[string](Cache, "visits", "visits/:page")

n, err := infra.Visits.Increment(ctx, "home", 1)
// a key with several params is a struct whose fields match them; ocel.ErrKVMiss on a missing key
```

```python
cache = ocel.kv("cache", eviction="allkeys-lru", memory="256mb")
visits = cache.counter("visits", "visits/:page")
profile = cache.json("profile", "profile/:userId", schema=Profile)

visits.increment(page="home")       # or increment(5, page="home"); *_async twins exist
p = profile.get(userId=user_id)
```

```rust
#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id", json = Session, ttl = "30d")]
pub struct SessionKey { pub id: String }

#[ocel(eviction = "allkeys-lru", memory = "256mb", entries = [SessionKey])]
pub sessions: ocel::Kv,
```

In TypeScript, `ioredis` is a peer dependency the app installs itself.

## Behaviour

- **Eviction:** `noeviction` is the default, so a full store refuses writes. A cache wants `allkeys-lru`.
- **TTLs:** `ttl` on an entry applies to every write; a write can override it.
- **Entry names:** names start with a letter. `client` and `connectionString` are reserved.

The SDK's own types and docstrings are the full API.
