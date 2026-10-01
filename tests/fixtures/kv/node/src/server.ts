import { randomUUID, timingSafeEqual } from "node:crypto";
import express, { type Response } from "express";
import { Redis } from "ioredis";
import { InvalidKVValueError } from "ocel/kv";
import { bounded, cache, evicting } from "../infra/index";
import { env } from "../infra/variables";

const APP_NAME = "web";
const PORT = Number(process.env.PORT ?? 3107);
const FILL_VALUES = 64;
const FILL_BYTES = 1024 * 1024;

const OCEL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/><circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/><path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" stroke-linecap="round"/></svg>\n`;
const OCEL_SVG_BYTES = Buffer.from(OCEL_SVG, "utf8");

const app = express();
app.use(express.json());

function sendFound(res: Response, value: unknown): void {
  if (value === undefined) {
    res.status(404).json({ error: "no such key" });
    return;
  }
  res.json({ value });
}

function sendInvalid(res: Response, error: unknown): void {
  if (error instanceof InvalidKVValueError) {
    res.status(422).json({ error: error.name, message: error.message });
    return;
  }
  throw error;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

app.get("/health", (_req, res) => {
  res.json({ ok: true, app: APP_NAME });
});

app.get("/ocel.svg", (_req, res) => {
  res.setHeader("content-type", "image/svg+xml");
  res.setHeader("content-length", String(OCEL_SVG_BYTES.byteLength));
  res.end(OCEL_SVG_BYTES);
});

app.get("/api/kv", (_req, res) => {
  const { hostname, port, username, password } = new URL(cache.connectionString);
  res.json({ host: hostname, port: Number(port), username, hasPassword: password.length > 0 });
});

app.get("/api/kv/ping", async (_req, res) => {
  res.json({ pong: await cache.client.ping() });
});

app.put("/api/kv/text/:key", async (req, res) => {
  await cache.text.set({ key: req.params.key }, req.body.value);
  res.status(204).end();
});

app.get("/api/kv/text/:key", async (req, res) => {
  sendFound(res, await cache.text.get({ key: req.params.key }));
});

app.put("/api/kv/counter/:key", async (req, res) => {
  await cache.counter.set({ key: req.params.key }, req.body.value);
  res.status(204).end();
});

app.get("/api/kv/counter/:key", async (req, res) => {
  sendFound(res, await cache.counter.get({ key: req.params.key }));
});

app.post("/api/kv/counter/:key/increment", async (req, res) => {
  res.json({ value: await cache.counter.increment({ key: req.params.key }, req.body?.by) });
});

app.post("/api/kv/counter/:key/decrement", async (req, res) => {
  res.json({ value: await cache.counter.decrement({ key: req.params.key }, req.body?.by) });
});

app.put("/api/kv/json/:key", async (req, res) => {
  try {
    await cache.json.set({ key: req.params.key }, req.body.value);
    res.status(204).end();
  } catch (error) {
    sendInvalid(res, error);
  }
});

app.get("/api/kv/json/:key", async (req, res) => {
  try {
    sendFound(res, await cache.json.get({ key: req.params.key }));
  } catch (error) {
    sendInvalid(res, error);
  }
});

app.get("/api/kv/lenient/:key", async (req, res) => {
  sendFound(res, await cache.lenient.get({ key: req.params.key }));
});

app.put("/api/kv/raw/:entry/:key", async (req, res) => {
  await cache.client.set(`${req.params.entry}/${req.params.key}`, req.body.raw);
  res.status(204).end();
});

app.post("/api/kv/list/:key/push", async (req, res) => {
  res.json({ length: await cache.list.push({ key: req.params.key }, req.body.values) });
});

app.post("/api/kv/list/:key/unshift", async (req, res) => {
  res.json({ length: await cache.list.unshift({ key: req.params.key }, req.body.values) });
});

app.post("/api/kv/list/:key/pop", async (req, res) => {
  sendFound(res, await cache.list.pop({ key: req.params.key }));
});

app.post("/api/kv/list/:key/shift", async (req, res) => {
  sendFound(res, await cache.list.shift({ key: req.params.key }));
});

app.get("/api/kv/list/:key", async (req, res) => {
  res.json({ values: await cache.list.slice({ key: req.params.key }) });
});

app.post("/api/kv/set/:key/add", async (req, res) => {
  res.json({ added: await cache.set.add({ key: req.params.key }, req.body.members) });
});

app.get("/api/kv/set/:key/has/:member", async (req, res) => {
  res.json({ has: await cache.set.has({ key: req.params.key }, req.params.member) });
});

app.get("/api/kv/set/:key", async (req, res) => {
  const members = await cache.set.values({ key: req.params.key });
  res.json({ members: [...members].sort() });
});

app.put("/api/kv/ttl/:key", async (req, res) => {
  const options = "ttl" in req.body ? { ttl: req.body.ttl } : undefined;
  await cache.fleeting.set({ key: req.params.key }, req.body.value, options);
  res.status(204).end();
});

app.get("/api/kv/ttl/:key", async (req, res) => {
  const value = await cache.fleeting.get({ key: req.params.key });
  if (value === undefined) {
    sendFound(res, value);
    return;
  }
  res.json({ value, ttlMs: await cache.client.pttl(`fleeting/${req.params.key}`) });
});

app.put("/api/kv/docs", async (req, res) => {
  await cache.docs.set({ id: req.body.id }, req.body.value);
  res.status(204).end();
});

app.post("/api/kv/docs/read", async (req, res) => {
  sendFound(res, await cache.docs.get({ id: req.body.id }));
});

app.get("/api/kv/meta/:id", async (req, res) => {
  sendFound(res, await cache.meta.get({ id: req.params.id }));
});

const filled = { bounded, evicting };

app.post("/api/kv/fill/:store", async (req, res) => {
  const store = filled[req.params.store as keyof typeof filled];
  if (!store) {
    res.status(404).json({ error: `no store named ${req.params.store}` });
    return;
  }
  await store.client.flushdb();
  const value = "v".repeat(FILL_BYTES);
  const prefix = randomUUID();
  let written = 0;
  let refused: string | undefined;
  for (; written < FILL_VALUES; written++) {
    try {
      await store.client.set(`${prefix}/${written}`, value);
    } catch (error) {
      refused = errorText(error);
      break;
    }
  }
  const kept =
    written === 0
      ? 0
      : await store.client.exists(...Array.from({ length: written }, (_, i) => `${prefix}/${i}`));
  await store.client.flushdb();
  res.json({ written, kept, ...(refused === undefined ? {} : { refused }) });
});

app.post("/api/kv/native/pubsub", async (req, res) => {
  const channel = String(req.body.channel);
  const subscriber = cache.client.duplicate();
  try {
    const received = new Promise<string>((resolve) => {
      subscriber.on("message", (from: string, message: string) => {
        if (from === channel) resolve(message);
      });
    });
    await subscriber.subscribe(channel);
    await cache.client.publish(channel, `hello ${channel}`);
    res.json({ received: await received });
  } finally {
    subscriber.disconnect();
  }
});

app.post("/api/kv/native/transaction", async (req, res) => {
  const key = String(req.body.key);
  const results = await cache.client.multi().set(key, "1").incr(key).get(key).exec();
  res.json({ results: (results ?? []).map(([error, value]) => error?.message ?? value) });
});

app.post("/api/kv/native/blocking-pop", async (req, res) => {
  const key = String(req.body.key);
  const waiting = cache.client.duplicate();
  try {
    const popped = waiting.blpop(key, 10);
    await new Promise((resolve) => setTimeout(resolve, 200));
    await cache.client.rpush(key, "woken");
    const answer = await popped;
    res.json({ popped: answer?.[1] ?? null });
  } finally {
    waiting.disconnect();
  }
});

app.get("/api/kv/unauthenticated", async (_req, res) => {
  const { hostname, port } = new URL(cache.connectionString);
  const bare = new Redis({
    host: hostname,
    port: Number(port),
    lazyConnect: true,
    enableReadyCheck: false,
    maxRetriesPerRequest: 0,
    retryStrategy: () => null,
  });
  bare.on("error", () => {});
  try {
    await bare.connect();
    await bare.set(`unauthenticated/${randomUUID()}`, "written");
    res.json({ refused: false });
  } catch (error) {
    res.json({ refused: true, error: errorText(error) });
  } finally {
    bare.disconnect();
  }
});

function isJourneyNonce(given: string | undefined): boolean {
  const expected = env.JOURNEY_NONCE;
  if (given === undefined) {
    return false;
  }
  const [givenBytes, expectedBytes] = [Buffer.from(given), Buffer.from(expected)];
  return (
    givenBytes.byteLength === expectedBytes.byteLength && timingSafeEqual(givenBytes, expectedBytes)
  );
}

app.get("/api/kv/password-report", (req, res) => {
  if (!isJourneyNonce(req.get("x-journey-nonce"))) {
    res.status(403).json({ error: "the password report needs the nonce the harness set" });
    return;
  }
  const { password } = new URL(cache.connectionString);
  const secret = decodeURIComponent(password);
  const environment = Object.entries(process.env)
    .filter(([, value]) => value?.includes(secret))
    .map(([name]) => name)
    .sort();
  res.json({ password: secret, environment });
});

app.listen(PORT, () => {
  console.log(`kv fixture listening on http://localhost:${PORT}`);
});
