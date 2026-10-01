import { createServer, type IncomingMessage, request, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { setTimeout as sleep } from "node:timers/promises";
import { type RunOptions, task } from "ocel/task";
import { topic } from "ocel/topic";

export type SentRequest = { procedure: string; name: string; payload: string };

export type Sighting = { kind: string; name: string; topic: string; payload: string };

export type RecordedEnvelope = { recordedEnvelope: string };

export type Names = { declared: string; bound: string; consumer?: string };

const PHYSICAL_PREFIX = "physical-";
const RUNTIME_ADDRESS = "OCEL_RUNTIME_ADDRESS";
const WORKER = "OCEL_WORKER";
const RECORDED_PROCEDURES = ["/Trigger", "/Send"];
const LOOPBACK = "127.0.0.1";
const LISTENS_WITHIN_MS = 30_000;
const LISTEN_POLL_MS = 50;

export const sighting = task("sighting", { run: (seen: Sighting) => seen });

export const envelope = task("envelope", { run: (recorded: RecordedEnvelope) => recorded });

async function recordSighting(tag: string, payload: unknown, { ctx }: RunOptions): Promise<void> {
  await sighting.trigger(
    { kind: ctx.kind, name: ctx.name, topic: ctx.topic, payload: JSON.stringify(payload) },
    { tags: [tag] },
  );
}

export const verbatim = task("verbatim", {
  run: async (payload: unknown, options) => {
    await recordSighting(options.ctx.id, payload, options);
    return payload;
  },
});

export const notices = topic<unknown>("notices");

export const noticeLog = notices.consumer("notice-log", (payload, options) =>
  recordSighting(options.ctx.message.id, payload, options),
);

function bindingKey(kind: "TASK" | "TOPIC", name: string): string {
  return `OCEL_RESOURCE_${kind}_${name}`;
}

function readBinding(key: string): Record<string, unknown> | undefined {
  const raw = process.env[key];
  if (!raw) return undefined;
  try {
    return JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return undefined;
  }
}

function renameBindings(...keys: string[]): void {
  for (const key of keys) {
    const binding = readBinding(key);
    if (binding && typeof binding.name === "string") {
      process.env[key] = JSON.stringify({ ...binding, name: `${PHYSICAL_PREFIX}${binding.name}` });
    }
  }
}

function readBoundName(key: string): string {
  const name = readBinding(key)?.name;
  return typeof name === "string" ? name : "";
}

export function readNames(): { task: Names; topic: Names } {
  return {
    task: { declared: verbatim.name, bound: readBoundName(bindingKey("TASK", verbatim.name)) },
    topic: {
      declared: notices.name,
      bound: readBoundName(bindingKey("TOPIC", notices.name)),
      consumer: noticeLog.name,
    },
  };
}

const sentRequests: SentRequest[] = [];

export function countRequests(): number {
  return sentRequests.length;
}

export function listRequestsSince(n: number): SentRequest[] {
  return sentRequests.slice(n);
}

function readVarint(body: Buffer, at: number): [number, number] {
  let value = 0;
  let shift = 0;
  let i = at;
  for (;;) {
    const byte = body[i] ?? 0;
    i += 1;
    value += (byte & 0x7f) * 2 ** shift;
    shift += 7;
    if (byte < 0x80 || i >= body.length) return [value, i];
  }
}

function readTopLevelBytes(body: Buffer): Map<number, Buffer> {
  const fields = new Map<number, Buffer>();
  let at = 0;
  while (at < body.length) {
    const [tag, next] = readVarint(body, at);
    const field = Math.floor(tag / 8);
    at = next;
    switch (tag % 8) {
      case 0:
        at = readVarint(body, at)[1];
        break;
      case 1:
        at += 8;
        break;
      case 5:
        at += 4;
        break;
      case 2: {
        const [length, start] = readVarint(body, at);
        fields.set(field, body.subarray(start, start + length));
        at = start + length;
        break;
      }
      default:
        throw new Error(`the request holds wire type ${tag % 8}, which is no protobuf one`);
    }
  }
  return fields;
}

function decodeRequest(procedure: string, contentType: string, body: Buffer): SentRequest {
  if (contentType !== "application/proto") {
    return {
      procedure,
      name: "",
      payload: `the request is ${contentType}, which this recorder does not read`,
    };
  }
  const fields = readTopLevelBytes(body);
  return {
    procedure,
    name: fields.get(1)?.toString("utf8") ?? "",
    payload: fields.get(2)?.toString("utf8") ?? "",
  };
}

async function readBody(req: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  return Buffer.concat(chunks);
}

function sendUpstream(req: IncomingMessage, body: Buffer, res: ServerResponse, to: URL) {
  return new Promise<void>((resolve, reject) => {
    const target = `${to.origin}${to.pathname.replace(/\/$/, "")}${req.url ?? "/"}`;
    const upstream = request(
      target,
      { method: req.method, headers: { ...req.headers, host: to.host } },
      (answer) => {
        res.writeHead(answer.statusCode ?? 502, answer.headers);
        answer.pipe(res);
        answer.on("end", resolve);
        answer.on("error", resolve);
      },
    );
    upstream.on("error", reject);
    res.on("close", () => {
      if (!res.writableFinished) upstream.destroy();
    });
    upstream.end(body);
  });
}

async function forward(req: IncomingMessage, body: Buffer, res: ServerResponse, to: URL) {
  const deadline = Date.now() + LISTENS_WITHIN_MS;
  for (;;) {
    try {
      await sendUpstream(req, body, res, to);
      return;
    } catch (error) {
      const refused = (error as NodeJS.ErrnoException).code === "ECONNREFUSED";
      if (!refused || Date.now() > deadline || res.destroyed) {
        if (!res.headersSent) res.writeHead(502).end(String(error));
        return;
      }
      await sleep(LISTEN_POLL_MS);
    }
  }
}

function listen(
  serve: (req: IncomingMessage, res: ServerResponse) => Promise<void>,
  port: number,
  host: string,
) {
  const server = createServer((req, res) => {
    serve(req, res).catch((error: unknown) => {
      if (!res.headersSent) res.writeHead(502).end(String(error));
    });
  });
  return new Promise<number>((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host, () => {
      server.unref();
      resolve((server.address() as AddressInfo).port);
    });
  });
}

async function findFreePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, LOOPBACK, resolve));
  const { port } = server.address() as AddressInfo;
  await new Promise((resolve) => server.close(resolve));
  return port;
}

async function recordRequests(): Promise<void> {
  const address = process.env[RUNTIME_ADDRESS];
  if (!address) return;
  const runtime = new URL(address);
  const port = await listen(
    async (req, res) => {
      const body = await readBody(req);
      const path = req.url ?? "";
      if (RECORDED_PROCEDURES.some((procedure) => path.endsWith(procedure))) {
        sentRequests.push(decodeRequest(path, req.headers["content-type"] ?? "", body));
      }
      await forward(req, body, res, runtime);
    },
    0,
    LOOPBACK,
  );
  process.env[RUNTIME_ADDRESS] = `http://${LOOPBACK}:${port}`;
}

async function recordEnvelope(body: Buffer): Promise<void> {
  const text = body.toString("utf8");
  const parsed = JSON.parse(text) as {
    execution?: string;
    message?: { id?: string };
    payload?: Partial<RecordedEnvelope>;
  };
  if (typeof parsed.payload?.recordedEnvelope === "string") return;
  const tags = [parsed.execution, parsed.message?.id].filter((tag): tag is string => !!tag);
  await envelope.trigger({ recordedEnvelope: text }, { tags });
}

async function recordEnvelopes(): Promise<void> {
  if (!process.env[WORKER]) return;
  const port = Number(process.env.PORT);
  const host = process.env.HOST || LOOPBACK;
  const behind = await findFreePort();
  process.env.PORT = String(behind);
  process.env.HOST = LOOPBACK;
  const worker = new URL(`http://${LOOPBACK}:${behind}`);
  await listen(
    async (req, res) => {
      const body = await readBody(req);
      await recordEnvelope(body);
      await forward(req, body, res, worker);
    },
    port,
    host,
  );
}

renameBindings(bindingKey("TASK", verbatim.name), bindingKey("TOPIC", notices.name));
await recordRequests();
await recordEnvelopes();
