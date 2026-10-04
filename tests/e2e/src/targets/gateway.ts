import {
  Agent,
  createServer as createHttpServer,
  request as httpRequest,
  type Server,
} from "node:http";
import { request as httpsRequest, Agent as SecureAgent } from "node:https";
import {
  connect as connectTcp,
  createServer as createNetServer,
  type Server as NetServer,
  type Socket,
} from "node:net";
import { connect as connectTls } from "node:tls";

export type Scheme = "http" | "https";

const EDGE_PORTS: Record<Scheme, number> = { http: 80, https: 443 };

export type Gateway = {
  serving: (hostname: string) => Promise<string>;
  socketServing: (hostname: string) => Promise<string>;
  close: () => Promise<void>;
};

const HEAD_END = "\r\n\r\n";

function listening(server: Server | NetServer): Promise<string> {
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

function namingHost(head: string, hostname: string): string {
  const [requestLine = "", ...headers] = head.split("\r\n");
  return [
    requestLine,
    ...headers.filter((line) => !/^host\s*:/i.test(line)),
    `host: ${hostname}`,
  ].join("\r\n");
}

function dialEdge(edge: Edge, hostname: string): Socket {
  return edge.tls
    ? connectTls({
        host: edge.host,
        port: edge.port,
        servername: hostname,
        rejectUnauthorized: false,
      })
    : connectTcp(edge.port, edge.host);
}

export type SocketForwarder = NetServer & { cut: () => void };

export function socketForwarder(edge: Edge, hostname: string): SocketForwarder {
  const open = new Set<Socket>();
  const server = createNetServer((client) => {
    open.add(client);
    client.on("close", () => open.delete(client));
    let head = Buffer.alloc(0);
    const reading = (chunk: Buffer) => {
      head = Buffer.concat([head, chunk]);
      const end = head.indexOf(HEAD_END);
      if (end < 0) return;
      client.off("data", reading);
      const upstream = dialEdge(edge, hostname);
      open.add(upstream);
      upstream.on("close", () => open.delete(upstream));
      upstream.on("error", () => client.destroy());
      client.on("error", () => upstream.destroy());
      upstream.write(
        `${namingHost(head.subarray(0, end).toString("latin1"), hostname)}${HEAD_END}`,
      );
      upstream.write(head.subarray(end + HEAD_END.length));
      client.pipe(upstream);
      upstream.pipe(client);
    };
    client.on("data", reading);
  });
  return Object.assign(server, {
    cut: () => {
      for (const socket of open) socket.destroy();
    },
  });
}

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
  const socketServers: SocketForwarder[] = [];
  const forwarders = new Map<string, Promise<string>>();
  const socketForwarders = new Map<string, Promise<string>>();
  const edge = { host: box, port: EDGE_PORTS[scheme], tls: scheme === "https" };

  return {
    serving(hostname) {
      let url = forwarders.get(hostname);
      if (!url) {
        const server = forwarder(edge, hostname);
        servers.push(server);
        url = listening(server);
        forwarders.set(hostname, url);
      }
      return url;
    },
    socketServing(hostname) {
      let url = socketForwarders.get(hostname);
      if (!url) {
        const server = socketForwarder(edge, hostname);
        socketServers.push(server);
        url = listening(server).then((listened) => listened.replace(/^http:/, "ws:"));
        socketForwarders.set(hostname, url);
      }
      return url;
    },
    close: async () => {
      await Promise.all([
        ...servers.map(closing),
        ...socketServers.map(
          (server) =>
            new Promise<void>((resolve) => {
              server.close(() => resolve());
              server.cut();
            }),
        ),
      ]);
    },
  };
}
