import {
  Agent,
  createServer as createHttpServer,
  request as httpRequest,
  type Server,
} from "node:http";
import { connect, type Socket } from "node:net";

const PROXY_PORT = 443;
const EDGE_PORT = 80;
const CONNECT_TIMEOUT_MS = 10_000;

export type Gateway = {
  tunnelUrl: string;
  serving: (hostname: string) => Promise<string>;
  close: () => Promise<void>;
};

function listening(server: Server): Promise<string> {
  return new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (typeof address !== "object" || address === null) {
        reject(new Error("the gateway could not read the port the kernel handed out"));
        return;
      }
      resolve(`http://127.0.0.1:${address.port}`);
    });
  });
}

function closing(server: Server): Promise<void> {
  return new Promise((resolve) => {
    server.closeAllConnections();
    server.close(() => resolve());
  });
}

function tunnel(box: string): Server {
  const server = createHttpServer((_, res) => {
    res.writeHead(405).end();
  });
  server.on("connect", (_req, client: Socket, head: Buffer) => {
    const both = () => {
      upstream.destroy();
      client.destroy();
    };
    const upstream = connect(PROXY_PORT, box, () => {
      upstream.setTimeout(0);
      client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
      if (head.length > 0) {
        upstream.write(head);
      }
      upstream.pipe(client);
      client.pipe(upstream);
    });
    upstream.setTimeout(CONNECT_TIMEOUT_MS, both);
    upstream.on("error", both);
    client.on("error", both);
  });
  return server;
}

export type Edge = { host: string; port: number };

export function forwarder(edge: Edge, hostname: string): Server {
  const unpooled = new Agent({ keepAlive: false });
  const server = createHttpServer((from, to) => {
    const upstream = httpRequest(
      {
        host: edge.host,
        port: edge.port,
        agent: unpooled,
        method: from.method,
        path: from.url,
        headers: { ...from.headers, host: hostname },
      },
      (answered) => {
        to.writeHead(answered.statusCode ?? 502, answered.headers);
        answered.pipe(to);
      },
    );
    upstream.on("error", (error) => {
      to.writeHead(502, { "content-type": "text/plain" }).end(String(error));
    });
    from.pipe(upstream);
  });
  server.on("close", () => unpooled.destroy());
  return server;
}

export async function openGateway(box: string): Promise<Gateway> {
  const servers: Server[] = [];
  const forwarders = new Map<string, Promise<string>>();

  const tunnelling = tunnel(box);
  servers.push(tunnelling);
  const tunnelUrl = await listening(tunnelling);

  return {
    tunnelUrl,
    serving(hostname) {
      let url = forwarders.get(hostname);
      if (!url) {
        const server = forwarder({ host: box, port: EDGE_PORT }, hostname);
        servers.push(server);
        url = listening(server);
        forwarders.set(hostname, url);
      }
      return url;
    },
    close: async () => {
      await Promise.all(servers.map(closing));
    },
  };
}
