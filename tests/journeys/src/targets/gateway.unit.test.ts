import { createServer, type Server } from "node:http";
import { afterEach, describe, expect, it } from "bun:test";
import { type Edge, forwarder } from "./gateway";

type Edging = { edge: Edge; sockets: () => number; reload: () => void; close: () => Promise<void> };

function opened(server: Server): Promise<{ host: string; port: number }> {
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

const standing: Array<() => Promise<void>> = [];

afterEach(async () => {
  while (standing.length > 0) {
    await standing.pop()?.();
  }
});

async function forwarding(edge: Edge): Promise<string> {
  const server = forwarder(edge, "app.localhost");
  standing.push(() => shut(server));
  const { host, port } = await opened(server);
  return `http://${host}:${port}`;
}

describe("forwarder", () => {
  it("opens a connection of its own for every request rather than pooling one", async () => {
    const box = await edging();
    standing.push(box.close);
    const url = await forwarding(box.edge);

    expect((await fetch(`${url}/one`)).status).toBe(204);
    expect((await fetch(`${url}/two`)).status).toBe(204);

    expect(box.sockets()).toBe(2);
  });

  it("serves what the edge served it after the edge closed every connection it held", async () => {
    const box = await edging();
    standing.push(box.close);
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
});
