import Fastify from "fastify";

export const FRAMEWORK = "fastify";

export const MARKER = `ocel-bench:${FRAMEWORK}:v1`;

export const app = Fastify();

app.get("/", (_req, reply) => {
  reply.type("text/plain").send(MARKER);
});

app.get("/health", (_req, reply) => {
  reply.send({ ok: true, framework: FRAMEWORK });
});

app.get<{ Params: { code: string } }>("/status/:code", (req, reply) => {
  reply.code(Number(req.params.code)).send({ framework: FRAMEWORK });
});

app.all("/echo/*", (req, reply) => {
  reply.send({
    framework: FRAMEWORK,
    method: req.method,
    path: req.url.split("?")[0],
    query: req.query,
    probeHeader: req.headers["x-ocel-probe"] ?? null,
    body: req.body ?? null,
  });
});
