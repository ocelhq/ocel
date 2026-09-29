import {
  Agent,
  createServer as createHttpServer,
  request as httpRequest,
  type Server,
} from "node:http";
import { request as httpsRequest, Agent as SecureAgent } from "node:https";

export type Scheme = "http" | "https";

const EDGE_PORTS: Record<Scheme, number> = { http: 80, https: 443 };

export type Gateway = {
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

export type Edge = { host: string; port: number; tls?: boolean };

export function forwarder(edge: Edge, hostname: string): Server {
  const unpooled = edge.tls
    ? new SecureAgent({ keepAlive: false, rejectUnauthorized: false, servername: hostname })
    : new Agent({ keepAlive: false });
  const request = edge.tls ? httpsRequest : httpRequest;
  const server = createHttpServer((from, to) => {
    const upstream = request(
      {
        host: edge.host,
        port: edge.port,
        servername: hostname,
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
      if (to.headersSent) {
        to.destroy(error);
        return;
      }
      to.writeHead(502, { "content-type": "text/plain" }).end(String(error));
    });
    from.pipe(upstream);
  });
  server.on("close", () => unpooled.destroy());
  return server;
}

export function openGateway(box: string, scheme: Scheme): Gateway {
  const servers: Server[] = [];
  const forwarders = new Map<string, Promise<string>>();

  return {
    serving(hostname) {
      let url = forwarders.get(hostname);
      if (!url) {
        const server = forwarder(
          { host: box, port: EDGE_PORTS[scheme], tls: scheme === "https" },
          hostname,
        );
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
