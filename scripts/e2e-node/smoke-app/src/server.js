// The e2e-node smoke app: an express app with nothing else declared —
// no queues, no crons, no other ocel resources — so the deploy realizes
// exactly one Lambda function. warmConcurrency fans the deploy's warm pass out
// against the account's own Lambda concurrency quota (10 on the disposable
// e2e account), and this app is kept to one function on purpose so that limit
// is never in play.
//
// Plain JS rather than TypeScript: the traced build type-strips TS but does
// not typecheck it, and this app has nothing worth typing — one less thing
// for the smoke app to depend on.
import express from "express";

const app = express();

// The harness's readiness probe, and also what the bytecode assertions burst
// against to force fresh sandboxes: express has no CDN or edge cache tier in
// front of it (unlike Next's worker), so any route reaches the Lambda on
// every request — no force-dynamic trick is needed here the way Next's
// TAG_PROBE_ROUTE needs one.
app.get("/health", (_req, res) => {
  res.json({ ok: true });
});

// SMOKE_MARKER proves the response body is this app's own render, not a
// cached or default one: assert-correctness reads it back verbatim, and a
// value that echoes the query string proves the route actually executed
// rather than answered from some earlier response.
const SMOKE_MARKER = "ocel-e2e-node-smoke-v1";

app.get("/echo", (req, res) => {
  res.json({ marker: SMOKE_MARKER, value: req.query.value ?? null, now: Date.now() });
});

const PORT = Number(process.env.PORT ?? 3000);
app.listen(PORT, () => {
  console.log(`e2e-node smoke app listening on http://localhost:${PORT}`);
});
