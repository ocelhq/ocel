import express from "express";
import { greeting } from "./greeting.js";
import { render } from "./lib/db";
import { banner } from "./config";
import { stamp } from "fake-dep";
import cjsDep from "cjs-dep";
import { label } from "workspace-pkg";

// Mirrors examples/express: the app calls listen() and never exports itself.
// The generated shim intercepts that listen() to capture the server.
// `./lib/db` and `./config` are extensionless relative imports (legal in TS,
// rejected by raw Node ESM) that the builder must rewrite; `express` (bare) and
// `./greeting.js` (already extensioned) must be left untouched. `fake-dep` is a
// traced ESM package whose own files use extensionless relative imports (like
// the ocel SDK's dist); `cjs-dep` is CJS and must be left untouched.
const app = express();
app.use(express.json());

app.get("/hello/:name", (req, res) => {
  res.json({ message: greeting(req.params.name) });
});

app.get("/render/:name", (req, res) => {
  res.json({ message: `${cjsDep.tag}${stamp(render(req.params.name))}`, banner });
});

// Exercises a workspace/symlinked package (real files outside node_modules).
app.get("/ws/:name", (req, res) => {
  res.json({ message: label(req.params.name) });
});

const PORT = Number(process.env.PORT ?? 3000);
app.listen(PORT, () => {
  console.log(`fixture express app listening on ${PORT}`);
});
