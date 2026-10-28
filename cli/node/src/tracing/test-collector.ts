import { execFile } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "vite";
import { afterAll, beforeAll } from "vitest";

const src = dirname(dirname(fileURLToPath(import.meta.url)));
const packageDir = dirname(src);
export const traceID = "4bf92f3577b34da6a3ce929d0e0e4736";
export const parentSpanID = "00f067aa0ba902b7";
export const traceparent = `00-${traceID}-${parentSpanID}-01`;

interface Attribute {
  key: string;
  value: {
    stringValue?: string;
    boolValue?: boolean;
    intValue?: number | string;
    arrayValue?: { values: Attribute["value"][] };
  };
}

export interface ReceivedSpan {
  scope?: string;
  traceId: string;
  spanId: string;
  parentSpanId?: string;
  name: string;
  attributes: Attribute[];
  events: string[];
  status?: { code?: number; message?: string };
}

interface Export {
  contentType: string;
  authorization: string;
  body: Buffer;
}

export const collectorToken = "run-token";

let workdir: string;
let collector: Server;
export let exports: Export[] = [];
export let endpoint: string;

export function useCollector(): void {
  beforeAll(async () => {
    workdir = await mkdtemp(join(tmpdir(), "ocel-tracing-"));
    collector = createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (chunk: Buffer) => chunks.push(chunk));
      req.on("end", () => {
        exports.push({
          contentType: req.headers["content-type"] ?? "",
          authorization: req.headers.authorization ?? "",
          body: Buffer.concat(chunks),
        });
        res.writeHead(200, { "content-type": "application/x-protobuf" }).end();
      });
    });
    await new Promise<void>((resolve) => collector.listen(0, "127.0.0.1", resolve));
    endpoint = `http://127.0.0.1:${(collector.address() as AddressInfo).port}`;
  });

  afterAll(async () => {
    await new Promise((resolve) => collector.close(resolve));
    await rm(workdir, { recursive: true, force: true });
  });
}

export function clearExports(): void {
  exports = [];
}

let entries = 0;

export async function bundled(source: string): Promise<string> {
  const name = `entry-${++entries}`;
  const entry = join(workdir, `${name}.ts`);
  await writeFile(entry, source.replaceAll("@src/", `${src}/`));
  await build({
    configFile: false,
    logLevel: "silent",
    root: packageDir,
    build: {
      ssr: entry,
      outDir: workdir,
      emptyOutDir: false,
      rollupOptions: { output: { entryFileNames: `${name}.mjs`, format: "es" } },
    },
    ssr: { noExternal: true, target: "node" },
  });
  return join(workdir, `${name}.mjs`);
}

interface Run {
  code: number | null;
  stdout: string;
  stderr: string;
}

function run(entry: string, env: Record<string, string>): Promise<Run> {
  return new Promise((resolve) => {
    const child = execFile(
      process.execPath,
      ["--enable-source-maps", entry],
      { env: { PATH: process.env.PATH ?? "", ...env }, cwd: workdir },
      (_err, stdout, stderr) => resolve({ code: child.exitCode, stdout, stderr }),
    );
  });
}

export function tracedEnv(): Record<string, string> {
  return {
    TRACEPARENT: traceparent,
    OCEL_TRACE_COLLECTOR_URL: `${endpoint}/v1/traces`,
    OCEL_TRACE_COLLECTOR_TOKEN: collectorToken,
  };
}

export async function tracedRun(source: string, env: Record<string, string> = tracedEnv()) {
  clearExports();
  const result = await run(await bundled(source), env);
  return { ...result, spans: exports.flatMap(spansOf) };
}

type Field = { number: number; bytes?: Uint8Array; varint?: bigint };

function fieldsOf(buf: Uint8Array): Field[] {
  const fields: Field[] = [];
  let at = 0;
  const varint = (): bigint => {
    let value = 0n;
    for (let shift = 0n; ; shift += 7n) {
      const byte = buf[at++] ?? 0;
      value |= BigInt(byte & 0x7f) << shift;
      if (byte < 0x80) return value;
    }
  };
  while (at < buf.length) {
    const tag = Number(varint());
    const number = tag >> 3;
    switch (tag & 7) {
      case 0:
        fields.push({ number, varint: varint() });
        break;
      case 1:
        at += 8;
        break;
      case 2: {
        const length = Number(varint());
        fields.push({ number, bytes: buf.subarray(at, at + length) });
        at += length;
        break;
      }
      case 5:
        at += 4;
        break;
      default:
        throw new Error(`unsupported wire type in tag ${tag}`);
    }
  }
  return fields;
}

const hex = (bytes?: Uint8Array) => Buffer.from(bytes ?? []).toString("hex");
const text = (bytes?: Uint8Array) => Buffer.from(bytes ?? []).toString("utf8");

function anyValueOf(buf: Uint8Array): Attribute["value"] {
  const [field] = fieldsOf(buf);
  switch (field?.number) {
    case 1:
      return { stringValue: text(field.bytes) };
    case 2:
      return { boolValue: field.varint === 1n };
    case 3:
      return { intValue: BigInt.asIntN(64, field.varint ?? 0n).toString() };
    case 5:
      return {
        arrayValue: {
          values: fieldsOf(field.bytes ?? new Uint8Array()).map((v) =>
            anyValueOf(v.bytes ?? new Uint8Array()),
          ),
        },
      };
    default:
      return {};
  }
}

function spanOf(buf: Uint8Array): ReceivedSpan {
  const span: ReceivedSpan = { traceId: "", spanId: "", name: "", attributes: [], events: [] };
  for (const field of fieldsOf(buf)) {
    if (field.number === 1) span.traceId = hex(field.bytes);
    if (field.number === 2) span.spanId = hex(field.bytes);
    if (field.number === 4 && field.bytes?.length) span.parentSpanId = hex(field.bytes);
    if (field.number === 5) span.name = text(field.bytes);
    if (field.number === 9) {
      const [key, value] = fieldsOf(field.bytes ?? new Uint8Array());
      span.attributes.push({
        key: text(key?.bytes),
        value: anyValueOf(value?.bytes ?? new Uint8Array()),
      });
    }
    if (field.number === 11) span.events.push(text(nested(field.bytes ?? new Uint8Array(), 2)[0]));
    if (field.number === 15) {
      const status: { code?: number; message?: string } = {};
      for (const part of fieldsOf(field.bytes ?? new Uint8Array())) {
        if (part.number === 2) status.message = text(part.bytes);
        if (part.number === 3) status.code = Number(part.varint);
      }
      span.status = status;
    }
  }
  return span;
}

function nested(buf: Uint8Array, number: number): Uint8Array[] {
  return fieldsOf(buf)
    .filter((f) => f.number === number && f.bytes)
    .map((f) => f.bytes as Uint8Array);
}

export function spansOf(received: Export): ReceivedSpan[] {
  if (received.contentType !== "application/x-protobuf") return [];
  return nested(received.body, 1)
    .flatMap((rs) => nested(rs, 2))
    .flatMap((ss) => {
      const [scope] = nested(ss, 1);
      const scopeName = scope && text(nested(scope, 1)[0]);
      return nested(ss, 2).map((buf) => ({ ...spanOf(buf), scope: scopeName }));
    });
}

function attributeValue(value: Attribute["value"]): unknown {
  if (value.arrayValue) return value.arrayValue.values.map(attributeValue);
  if (value.intValue !== undefined) return Number(value.intValue);
  if (value.boolValue !== undefined) return value.boolValue;
  return value.stringValue;
}

export function attributes(span: ReceivedSpan): Record<string, unknown> {
  return Object.fromEntries(span.attributes.map((a) => [a.key, attributeValue(a.value)]));
}

export function named(spans: ReceivedSpan[], name: string): ReceivedSpan {
  const span = spans.find((s) => s.name === name);
  if (!span) throw new Error(`no span named ${name} in ${spans.map((s) => s.name).join(", ")}`);
  return span;
}
