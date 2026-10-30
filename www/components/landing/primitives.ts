// The landing page's resource tiles. Every declaration and call here is the one the SDK
// docs (content/docs/sdk/*.mdx) show, checked against each SDK's exports: packages/ocel,
// sdk/, python/ocel and crates/ocel-sdk.
//
// Each tile has two files, because a resource is declared in one place and used in another:
// TypeScript, Go and Python declare in the discovery folder (infra/ by default), which `ocel dev`,
// `ocel deploy` and `ocel preview` run on their own before anything is provisioned, so the
// app reads from it elsewhere; Rust declares on a struct in the app crate and loads it at
// runtime. Identifiers a snippet leaves undefined (readCaller, ownsOrder, OrderEvent, …)
// stand for the reader's own code.

export const languages = ["TypeScript", "Go", "Python", "Rust"] as const;
export type Language = (typeof languages)[number];

export const shikiLang: Record<Language, "typescript" | "go" | "python" | "rust"> = {
  TypeScript: "typescript",
  Go: "go",
  Python: "python",
  Rust: "rust",
};

export type Snippet = { file: string; code: string };
export type Pair = { declare: Snippet; use: Snippet };

export type PrimitiveName = "postgres" | "bucket" | "kv" | "task" | "topic" | "realtime";

export type Primitive = {
  name: PrimitiveName;
  what: string;
  docs: string;
  snippets: Record<Language, Pair>;
};

const ts = (code: string, use: string, file = "infra/index.ts", from = "../infra") => ({
  declare: { file, code },
  use: { file: "src/server.ts", code: use.replace("{from}", from) },
});
const go = (code: string, use: string) => ({
  declare: { file: "infra/infra.go", code },
  use: { file: "main.go", code: `import "example.com/notes/infra"\n\n${use}` },
});
const py = (code: string, use: string) => ({
  declare: { file: "infra/__init__.py", code },
  use: { file: "main.py", code: use },
});
const rs = (code: string, use: string) => ({
  declare: { file: "src/main.rs", code },
  use: { file: "src/routes.rs", code: use },
});

export const primitives: Primitive[] = [
  {
    name: "postgres",
    what: "A Postgres database, and a pool to query it.",
    docs: "/docs/sdk/postgres",
    snippets: {
      TypeScript: ts(
        `import { postgres } from "ocel/postgres";

export const db = postgres("main");`,
        `import { db } from "{from}";

const { rows } = await db.query("select * from notes");`,
      ),
      Go: go(
        `package infra

import "ocel.dev/postgres"

var DB = postgres.New("main")`,
        `pool, err := infra.DB.Pool(ctx)
rows, err := pool.Query(ctx, "select * from notes")`,
      ),
      Python: py(
        `import ocel

db = ocel.postgres("main")`,
        `from infra import db

rows = await db.fetch("select * from notes")`,
      ),
      Rust: rs(
        `#[derive(ocel::Resources)]
pub struct Infra {
    pub main: ocel::Postgres,
}`,
        `let infra = Infra::load()?;
let pool = infra.main.pool().await?;`,
      ),
    },
  },
  {
    name: "bucket",
    what: "File storage: put, get, list and delete.",
    docs: "/docs/sdk/bucket",
    snippets: {
      TypeScript: ts(
        `import { bucket } from "ocel/bucket";

export const uploads = bucket("uploads");`,
        `import { uploads } from "{from}";

await uploads.put("reports/q3.pdf", bytes);`,
      ),
      Go: go(
        `package infra

import "ocel.dev/bucket"

var Uploads = bucket.New("uploads")`,
        `err := infra.Uploads.WriteAll(ctx, "reports/q3.pdf", data)`,
      ),
      Python: py(
        `import ocel

uploads = ocel.bucket("uploads")`,
        `from infra import uploads

uploads.put("reports/q3.pdf", data)`,
      ),
      Rust: rs(
        `#[derive(ocel::Resources)]
pub struct Infra {
    pub uploads: ocel::Bucket,
}`,
        `let infra = Infra::load()?;
infra.uploads.put("reports/q3.pdf", data).await?;`,
      ),
    },
  },
  {
    name: "kv",
    what: "A key-value store with typed keys and TTLs.",
    docs: "/docs/sdk/kv",
    snippets: {
      TypeScript: ts(
        `import { kv } from "ocel/kv";

export const cache = kv("cache", {
  entries: { session: kv.text("session/:id") },
});`,
        `import { cache } from "{from}";

await cache.session.set({ id: "abc" }, token, { ttl: "1h" });`,
      ),
      Go: go(
        `package infra

import "ocel.dev/kv"

var Cache = kv.New("cache")
var Session = kv.NewText[string](Cache, "session", "session/:id")`,
        `err := infra.Session.Set(ctx, "abc", token, kv.TTL(time.Hour))`,
      ),
      Python: py(
        `import ocel

cache = ocel.kv("cache")
session = cache.text("session", "session/:id")`,
        `from infra import session

session.set(token, id="abc", ttl="1h")`,
      ),
      Rust: rs(
        `#[derive(ocel::KvKey)]
#[ocel(pattern = "session/:id", text)]
struct Session { id: String }

#[derive(ocel::Resources)]
pub struct Infra {
    #[ocel(entries = [Session])]
    pub cache: ocel::Kv,
}`,
        `let infra = Infra::load()?;
let entry = infra.cache.entry(Session { id: "abc".into() });
entry.set(token).ttl(Duration::from_secs(3600)).await?;`,
      ),
    },
  },
  {
    name: "task",
    what: "Background jobs you trigger now or later.",
    docs: "/docs/sdk/task",
    snippets: {
      TypeScript: ts(
        `import { task } from "ocel/task";

export const resize = task("resize-image", {
  run: async (image: Image) => image.url.length,
});`,
        `import { resize } from "{from}";

await resize.trigger({ url }, { delay: "5m" });`,
      ),
      Go: go(
        `package infra

import "ocel.dev/task"

var Resize = task.New("resize-image",
	func(ctx context.Context, image Image) (int, error) {
		return len(image.URL), nil
	})`,
        `run, err := infra.Resize.Trigger(ctx,
	infra.Image{URL: url})`,
      ),
      Python: py(
        `import ocel

@ocel.task("resize-image")
async def resize(image: dict, ctx: ocel.RunContext) -> int:
    return len(image["url"])`,
        `from infra import resize

run = resize.trigger({"url": url}, delay=timedelta(minutes=5))`,
      ),
      Rust: rs(
        `#[ocel::task(name = "resize-image")]
async fn resize(
    image: Image,
    _run: &ocel::Run,
) -> Result<usize, ocel::RunError> {
    Ok(image.url.len())
}`,
        `let run = resize.trigger(Image { url }).await?;`,
      ),
    },
  },
  {
    name: "topic",
    what: "Pub/sub, with consumers that run as workers.",
    docs: "/docs/sdk/topic",
    snippets: {
      TypeScript: ts(
        `import { topic } from "ocel/topic";

export const orders = topic<Order>("orders");

export const audit = orders.consumer("audit", async (order) => {
  console.log("order placed", order.id);
});`,
        `import { orders } from "{from}";

await orders.send({ id: "o_123" });`,
      ),
      Go: go(
        `package infra

import "ocel.dev/topic"

var Orders = topic.New[Order]("orders")

var Audit = Orders.Consumer("audit",
	func(ctx context.Context, order Order) error {
		log.Println("order placed", order.ID)
		return nil
	})`,
        `messageID, err := infra.Orders.Send(ctx,
	infra.Order{ID: "o_123"})`,
      ),
      Python: py(
        `import ocel

orders = ocel.topic("orders")

@orders.consumer("audit")
async def audit(order: dict, ctx: ocel.RunContext) -> None:
    print("order placed", order["id"])`,
        `from infra import orders

orders.send({"id": "o_123"})`,
      ),
      Rust: rs(
        `#[derive(ocel::Resources)]
pub struct Infra {
    pub orders: ocel::Topic<Order>,
}

#[ocel::consumer(topic = Infra::ORDERS)]
async fn audit(
    order: Order,
    _run: &ocel::Run,
) -> Result<(), ocel::RunError> {
    println!("order placed {}", order.id);
    Ok(())
}`,
        `let infra = Infra::load()?;
infra.orders.send(Order { id: "o_123".into() }).await?;`,
      ),
    },
  },
  {
    name: "realtime",
    what: "Typed channels pushed to the browser.",
    docs: "/docs/sdk/realtime",
    snippets: {
      TypeScript: ts(
        `import { realtime } from "ocel/realtime";

export const rt = realtime("app", {
  authorize,
  channels: { "orders/:orderId": { schema, subscribe } },
});`,
        `import { rt } from "{from}";

await rt.publish("orders/:orderId", {
  params: { orderId },
  body: { status: "shipped" },
});`,
        "infra/realtime.ts",
        "../infra/realtime",
      ),
      Go: {
        declare: {
          file: "infra/realtime.go",
          code: `package infra

import "ocel.dev/realtime"

var Live = realtime.New("app", realtime.Authorize(readCaller))

var Orders = realtime.NewChannel[OrderEvent, OrderParams](Live,
	"orders/:orderId", realtime.ChannelSubscribe(ownsOrder))`,
        },
        use: {
          file: "main.go",
          code: `import "example.com/notes/infra"

err := infra.Orders.Publish(ctx,
	infra.OrderParams{OrderID: id},
	infra.OrderEvent{Status: "shipped"})`,
        },
      },
      Python: py(
        `import ocel

live = ocel.realtime("app", authorize=read_caller)

@live.channel("orders/:orderId", schema=OrderEvent)
def orders(ctx: ocel.RuleContext) -> bool:
    return owns_order(ctx.auth.id, ctx.params["orderId"])`,
        `from infra import orders

orders.publish({"status": "shipped"}, orderId=order_id)`,
      ),
      Rust: rs(
        `#[derive(ocel::Channel)]
#[ocel(
    realtime = "app",
    pattern = "orders/:orderId",
    event = OrderEvent
)]
struct Orders { orderId: String }`,
        `rt.publish(
    &Orders { orderId: id },
    &OrderEvent { status: "shipped".into() },
)
.await?;`,
      ),
    },
  },
];
