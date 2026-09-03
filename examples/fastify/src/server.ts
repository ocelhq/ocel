import Fastify, { type FastifyReply, type FastifyRequest } from "fastify";
import { createRouteHandler } from "ocel/blob";
import { pg, uploads } from "../ocel/index";
import { MAX_BODY, probes } from "./probes";

const APP_NAME = process.env.APP_NAME ?? "web";
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

const upload = createRouteHandler(uploads);

function webRequest(request: FastifyRequest): Request {
  const host = request.headers.host ?? `127.0.0.1:${PORT}`;
  const url = `${request.protocol}://${host}${request.url}`;
  if (request.method === "GET" || request.body === undefined) {
    return new Request(url, { method: request.method });
  }
  return new Request(url, {
    method: request.method,
    headers: { "content-type": "application/json" },
    body: JSON.stringify(request.body),
  });
}

async function sendResponse(reply: FastifyReply, webRes: Response): Promise<void> {
  reply.code(webRes.status);
  webRes.headers.forEach((value, key) => {
    if (key !== "content-length") {
      reply.header(key, value);
    }
  });
  await reply.send(Buffer.from(await webRes.arrayBuffer()));
}

app.get("/health", async () => ({ ok: true, app: APP_NAME }));

app.get("/ocel.svg", async (_request, reply) =>
  reply.header("content-type", "image/svg+xml").send(OCEL_SVG_BYTES),
);

app.register(probes, { prefix: "/api/probes" });

app.route({
  method: ["GET", "POST"],
  url: "/api/upload",
  handler: async (request, reply) => {
    const web = webRequest(request);
    const webRes =
      request.method === "GET" ? await upload.GET(web) : await upload.POST(web, request);
    await sendResponse(reply, webRes);
  },
});

app.post<{ Body: { title?: unknown } }>("/api/todos", async (request, reply) => {
  const title = request.body?.title;
  if (typeof title !== "string" || title.length === 0) {
    return reply.code(400).send({ error: "title is required" });
  }
  const { rows } = await pg.query(
    "INSERT INTO todos (title) VALUES ($1) RETURNING id, title, done",
    [title],
  );
  return reply.code(201).send(rows[0]);
});

app.get("/api/todos", async () => {
  const { rows } = await pg.query("SELECT id, title, done FROM todos ORDER BY id");
  return rows;
});

app.get<{ Params: { id: string } }>("/api/todos/:id", async (request, reply) => {
  const { rows } = await pg.query(
    "SELECT id, title, done FROM todos WHERE id = $1",
    [Number(request.params.id)],
  );
  if (rows.length === 0) {
    return reply.code(404).send({ error: "not found" });
  }
  return rows[0];
});

app.delete<{ Params: { id: string } }>("/api/todos/:id", async (request, reply) => {
  const { rowCount } = await pg.query("DELETE FROM todos WHERE id = $1", [
    Number(request.params.id),
  ]);
  if (!rowCount) {
    return reply.code(404).send({ error: "not found" });
  }
  return reply.code(204).send();
});

app.get("/api/documents", async () => {
  const { rows } = await pg.query(
    "SELECT id, key, name, mime_type, size, owner_id FROM documents ORDER BY id",
  );
  return rows;
});

app.listen({ port: PORT, host: "0.0.0.0" }, (error, address) => {
  if (error) {
    console.error(error);
    process.exit(1);
  }
  console.log(`fastify example listening on ${address}`);
});
