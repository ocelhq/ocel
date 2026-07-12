import express from "express";
import { greeting } from "./greeting.js";
import { render } from "./lib/db";
import { banner } from "./config";
import { stamp } from "fake-dep";
import cjsDep from "cjs-dep";
import { label } from "workspace-pkg";

// Mirrors examples: the app exports itself as the default export; the nodert
// runtime imports it and serves it (no listen() here).
// `./lib/db` and `./config` are extensionless relative imports (legal in TS,
// rejected by raw Node ESM) that the builder must rewrite; `express` (bare) and
// `./greeting.js` (already extensioned) must be left untouched. `fake-dep` is a
// traced ESM package whose own files use extensionless relative imports (like
// the ocel SDK's dist); `cjs-dep` is CJS and must be left untouched.
const app = express();
app.use(express.json());

app.get("/hello/:name", (req, res) => {
  // Modern syntax (?. and ??) the transpiler must preserve verbatim for the
  // nodejs24.x runtime — not downlevel to helper functions.
  const name = req.params?.name ?? "world";
  res.json({ message: greeting(name) });
});

app.get("/render/:name", (req, res) => {
  res.json({ message: `${cjsDep.tag}${stamp(render(req.params.name))}`, banner });
});

// Exercises a workspace/symlinked package (real files outside node_modules).
app.get("/ws/:name", (req, res) => {
  res.json({ message: label(req.params.name) });
});

export default app;
