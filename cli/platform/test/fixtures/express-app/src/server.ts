import express from "express";
import { greeting } from "./greeting.js";
import { render } from "./lib/db";
import { banner } from "./config";
import { stamp } from "fake-dep";
import cjsDep from "cjs-dep";
import { label } from "workspace-pkg";

const app = express();
app.use(express.json());

app.get("/hello/:name", (req, res) => {
  const name = req.params?.name ?? "world";
  res.json({ message: greeting(name) });
});

app.get("/render/:name", (req, res) => {
  res.json({ message: `${cjsDep.tag}${stamp(render(req.params.name))}`, banner });
});

app.get("/ws/:name", (req, res) => {
  res.json({ message: label(req.params.name) });
});

export default app;
