import type { Transport } from "@connectrpc/connect";
import {
  type UniversalClientRequest,
  type UniversalClientResponse,
  validateReadWriteMaxBytes,
} from "@connectrpc/connect/protocol";
import { createTransport } from "@connectrpc/connect/protocol-connect";
import type { ConnectTransportOptions } from "./connect-transport.js";

async function readBody(body: AsyncIterable<Uint8Array>): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  let length = 0;
  for await (const chunk of body) {
    chunks.push(chunk);
    length += chunk.byteLength;
  }
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return bytes;
}

async function* streamChunks(stream: ReadableStream<Uint8Array> | null): AsyncIterable<Uint8Array> {
  if (!stream) return;
  const reader = stream.getReader();
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) return;
      yield value;
    }
  } finally {
    reader.releaseLock();
  }
}

async function sendFetchRequest(req: UniversalClientRequest): Promise<UniversalClientResponse> {
  const res = await fetch(req.url, {
    method: req.method,
    headers: req.header,
    signal: req.signal,
    body: req.body === undefined ? undefined : await readBody(req.body),
  });
  const header = new Headers(res.headers);
  if (header.has("content-encoding")) {
    header.delete("content-encoding");
    header.delete("content-length");
  }
  return {
    status: res.status,
    header,
    body: streamChunks(res.body),
    trailer: new Headers(),
  };
}

/** A Connect transport over the edge runtime's fetch, which loads no node builtin. */
export function createConnectTransport(options: ConnectTransportOptions): Transport {
  return createTransport({
    ...options,
    httpClient: sendFetchRequest,
    useBinaryFormat: true,
    acceptCompression: [],
    sendCompression: null,
    ...validateReadWriteMaxBytes(undefined, undefined, undefined),
  });
}
