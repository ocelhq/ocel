import { readFileSync } from "node:fs";
import express from "express";

const { version } = JSON.parse(
  readFileSync(new URL("./package.json", import.meta.url), "utf8"),
);

const serve = (response) => response.type("text/plain").send(version);

const app = express();

app.get("/healthz", (_request, response) => serve(response));

app.get("/hold", (request, response) => {
  setTimeout(() => serve(response), Number(request.query.s ?? 0) * 1000);
});

app.get("/", (_request, response) => serve(response));

app.listen(Number(process.env.PORT) || 3000);
