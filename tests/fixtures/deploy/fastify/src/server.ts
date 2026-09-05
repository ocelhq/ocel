import Fastify from "fastify";
import { MAX_BODY, probes } from "./probes";

const APP_NAME = "web";
const PORT = Number(process.env.PORT ?? 3104);

const OCEL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="ocel"><rect width="64" height="64" rx="14" fill="#0b0f14"/><circle cx="24" cy="27" r="5" fill="#f2b705"/><circle cx="42" cy="27" r="5" fill="#f2b705"/><path d="M20 42c4 5 20 5 24 0" stroke="#f2b705" stroke-width="4" fill="none" stroke-linecap="round"/></svg>\n`;
const OCEL_SVG_BYTES = Buffer.from(OCEL_SVG, "utf8");

const app = Fastify({ bodyLimit: MAX_BODY });

function statusOf(error: unknown): number {
  const raw = (error as { statusCode?: unknown; status?: unknown }) ?? {};
  const status = typeof raw.statusCode === "number" ? raw.statusCode : raw.status;
  return typeof status === "number" && Number.isInteger(status) && status >= 400 && status <= 599
    ? status
    : 500;
}

app.setErrorHandler(async (error, _request, reply) => {
  console.error(error);
  if (reply.raw.headersSent) {
    reply.raw.end();
    return reply;
  }
  const status = statusOf(error);
  return reply
    .code(status)
    .send({ error: status === 500 ? "internal error" : "bad request" });
});

app.get("/health", async () => ({ ok: true, app: APP_NAME }));

app.get("/ocel.svg", async (_request, reply) =>
  reply.header("content-type", "image/svg+xml").send(OCEL_SVG_BYTES),
);

app.register(probes, { prefix: "/api/probes" });

app.listen({ port: PORT, host: "0.0.0.0" }, (error, address) => {
  if (error) {
    console.error(error);
    process.exit(1);
  }
  console.log(`fastify listening on ${address}`);
});
