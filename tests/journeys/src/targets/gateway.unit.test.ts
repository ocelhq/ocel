import { afterEach, describe, expect, it } from "bun:test";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { createServer as createSecureServer } from "node:https";
import { createServer as createNetServer, type Server as NetServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import type { TLSSocket } from "node:tls";
import { type Edge, forwarder } from "./gateway";

type Edging = { edge: Edge; sockets: () => number; reload: () => void; close: () => Promise<void> };

function opened(server: Server | NetServer): Promise<{ host: string; port: number }> {
  return new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (typeof address !== "object" || address === null) {
        reject(new Error("the server bound no port"));
        return;
      }
      resolve({ host: "127.0.0.1", port: address.port });
    });
  });
}

function shut(server: Server): Promise<void> {
  return new Promise((resolve) => {
    server.closeAllConnections();
    server.close(() => resolve());
  });
}

async function edging(): Promise<Edging> {
  const seen = new Set<number>();
  const box = createServer((request, response) => {
    seen.add(request.socket.remotePort ?? 0);
    response.writeHead(204).end();
  });
  const edge = await opened(box);
  return {
    edge,
    sockets: () => seen.size,
    reload: () => box.closeIdleConnections(),
    close: () => shut(box),
  };
}

const closers: Array<() => Promise<void>> = [];

afterEach(async () => {
  while (closers.length > 0) {
    await closers.pop()?.();
  }
});

function selfSigned(hostname: string): { key: Buffer; cert: Buffer } {
  const dir = mkdtempSync(path.join(tmpdir(), "gateway-"));
  const made = spawnSync("openssl", [
    "req",
    "-x509",
    "-newkey",
    "ec",
    "-pkeyopt",
    "ec_paramgen_curve:prime256v1",
    "-nodes",
    "-days",
    "1",
    "-subj",
    `/CN=${hostname}`,
    "-addext",
    `subjectAltName=DNS:${hostname}`,
    "-keyout",
    path.join(dir, "key.pem"),
    "-out",
    path.join(dir, "cert.pem"),
  ]);
  if (made.status !== 0) {
    throw new Error(`openssl could not make a certificate: ${made.stderr}`);
  }
  return {
    key: readFileSync(path.join(dir, "key.pem")),
    cert: readFileSync(path.join(dir, "cert.pem")),
  };
}

async function forwarding(edge: Edge): Promise<string> {
  const server = forwarder(edge, "app.localhost");
  closers.push(() => shut(server));
  const { host, port } = await opened(server);
  return `http://${host}:${port}`;
}

describe("forwarder", () => {
  it("opens a connection of its own for every request rather than pooling one", async () => {
    const box = await edging();
    closers.push(box.close);
    const url = await forwarding(box.edge);

    expect((await fetch(`${url}/one`)).status).toBe(204);
    expect((await fetch(`${url}/two`)).status).toBe(204);

    expect(box.sockets()).toBe(2);
  });

  it("serves what the edge served it after the edge closed every connection it had open", async () => {
    const box = await edging();
    closers.push(box.close);
    const url = await forwarding(box.edge);

    expect((await fetch(`${url}/before`)).status).toBe(204);
    box.reload();

    expect((await fetch(`${url}/after`)).status).toBe(204);
  });

  it("answers 502 for an edge that answers nothing, and for nothing else", async () => {
    const box = await edging();
    await box.close();
    const url = await forwarding(box.edge);

    expect((await fetch(`${url}/gone`)).status).toBe(502);
  });

  it("reaches an edge that serves only https over tls, naming the app's hostname to it", async () => {
    let named: string | false | null | undefined;
    let host: string | undefined;
    const box = createSecureServer(selfSigned("app.localhost"), (request, response) => {
      named = (request.socket as TLSSocket).servername;
      host = request.headers.host;
      response.writeHead(204).end();
    });
    closers.push(() => shut(box));
    const url = await forwarding({ ...(await opened(box)), tls: true });

    expect((await fetch(`${url}/secure`)).status).toBe(204);
    expect(named).toBe("app.localhost");
    expect(host).toBe("app.localhost");
  });

  it("cuts a response short when the edge drops it midway, rather than crashing", async () => {
    const box = createNetServer((socket) => {
      socket.once("data", () => {
        socket.write("HTTP/1.1 200 OK\r\ncontent-length: 100\r\n\r\npartial");
        setTimeout(() => socket.resetAndDestroy(), 20);
      });
    });
    const edge = await opened(box);
    closers.push(() => new Promise((resolve) => box.close(() => resolve())));
    const url = await forwarding(edge);

    const answered = await fetch(`${url}/dropped`);
    expect(answered.status).toBe(200);
    await expect(answered.text()).rejects.toThrow();
    expect((await fetch(`${url}/dropped`)).status).toBe(200);
  });
});
