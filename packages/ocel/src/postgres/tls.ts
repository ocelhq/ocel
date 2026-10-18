import { readFileSync } from "node:fs";
import { connect as connectTcp, type Socket } from "node:net";
import { Duplex } from "node:stream";
import {
  type ConnectionOptions,
  checkServerIdentity,
  connect as connectTls,
  type TLSSocket,
} from "node:tls";
import {
  type PostgresProperties,
  PostgresTlsMode,
} from "../gen/proto/common/bindings/v1/bindings_pb.js";

export type SslMode = "disable" | "allow" | "prefer" | "require" | "verify-ca" | "verify-full";

const sslModes: readonly string[] = [
  "disable",
  "allow",
  "prefer",
  "require",
  "verify-ca",
  "verify-full",
];

export interface TlsPolicy {
  mode: SslMode;
  direct: boolean;
  ca: string | undefined;
  serverName: string;
  cert: string | undefined;
  key: string | undefined;
}

const modeOfTls: Record<PostgresTlsMode, SslMode> = {
  [PostgresTlsMode.UNSPECIFIED]: "prefer",
  [PostgresTlsMode.REQUIRE]: "require",
  [PostgresTlsMode.VERIFY_FULL]: "verify-full",
};

const tlsParams = [
  "ssl",
  "sslmode",
  "sslrootcert",
  "sslcert",
  "sslkey",
  "sslnegotiation",
  "uselibpqcompat",
];

export function tlsPolicyOf(properties: PostgresProperties): TlsPolicy {
  const policy: TlsPolicy = {
    mode: modeOfTls[properties.tlsMode],
    direct: false,
    ca: undefined,
    serverName: properties.tlsServerName,
    cert: undefined,
    key: undefined,
  };
  if (properties.url) {
    readUrlParams(policy, queryOf(properties.url));
  }
  if (properties.tlsCa) {
    policy.ca = properties.tlsCa;
  }
  return policy;
}

function readUrlParams(policy: TlsPolicy, params: URLSearchParams) {
  const mode = params.get("sslmode") ?? "prefer";
  if (!sslModes.includes(mode)) {
    throw new Error(`the postgres binding's url names sslmode=${mode}, which libpq does not know`);
  }
  policy.mode = mode as SslMode;
  policy.direct = params.get("sslnegotiation") === "direct";
  if (policy.direct && policy.mode === "prefer") {
    policy.mode = "require";
  }
  const rootCert = params.get("sslrootcert");
  if (rootCert === "system") {
    policy.mode = "verify-full";
  } else if (rootCert) {
    policy.ca = readFileSync(rootCert, "utf8");
    if (policy.mode === "require") {
      policy.mode = "verify-ca";
    }
  }
  const cert = params.get("sslcert");
  const key = params.get("sslkey");
  if (Boolean(cert) !== Boolean(key)) {
    throw new Error("the postgres binding's url names one of sslcert and sslkey without the other");
  }
  if (cert && key) {
    policy.cert = readFileSync(cert, "utf8");
    policy.key = readFileSync(key, "utf8");
  }
}

function queryOf(url: string): URLSearchParams {
  const at = url.indexOf("?");
  return new URLSearchParams(at === -1 ? "" : url.slice(at + 1));
}

export function withoutTlsParams(url: string): string {
  const at = url.indexOf("?");
  if (at === -1) return url;
  const params = queryOf(url);
  for (const param of tlsParams) {
    params.delete(param);
  }
  const rest = params.toString();
  return rest ? `${url.slice(0, at)}?${rest}` : url.slice(0, at);
}

export function tlsOptionsOf(policy: TlsPolicy, host: string): ConnectionOptions {
  const identity = policy.serverName || host;
  const options: ConnectionOptions = {
    ...(isAddress(identity) ? {} : { servername: identity }),
    ...(policy.ca ? { ca: policy.ca } : {}),
    ...(policy.cert && policy.key ? { cert: policy.cert, key: policy.key } : {}),
    ...(policy.direct ? { ALPNProtocols: ["postgresql"] } : {}),
  };
  switch (policy.mode) {
    case "verify-full":
      return {
        ...options,
        rejectUnauthorized: true,
        checkServerIdentity: (_, certificate) => checkServerIdentity(identity, certificate),
      };
    case "verify-ca":
      return { ...options, rejectUnauthorized: true, checkServerIdentity: () => undefined };
    default:
      return { ...options, rejectUnauthorized: false };
  }
}

function isAddress(host: string): boolean {
  return /^[\d.]+$/.test(host) || host.includes(":");
}

const sslRequest = Buffer.from([0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f]);

export class PostgresSocket extends Duplex {
  private socket: Socket | TLSSocket | undefined;
  private noDelay = true;
  private keepAlive: [boolean, number] | undefined;

  constructor(private readonly policy: TlsPolicy) {
    super();
    this.cork();
  }

  setNoDelay(noDelay = true): this {
    this.noDelay = noDelay;
    this.socket?.setNoDelay(noDelay);
    return this;
  }

  setKeepAlive(enable = false, initialDelay = 0): this {
    this.keepAlive = [enable, initialDelay];
    this.socket?.setKeepAlive(enable, initialDelay);
    return this;
  }

  ref(): this {
    this.socket?.ref();
    return this;
  }

  unref(): this {
    this.socket?.unref();
    return this;
  }

  connect(port: number, host: string): this {
    const tcp = connectTcp(port, host);
    tcp.setNoDelay(this.noDelay);
    tcp.on("error", (err) => this.destroy(err));
    tcp.once("close", () => {
      if (!this.socket)
        this.destroy(new Error(`the postgres server at ${host} closed the connection`));
    });
    tcp.once("connect", () => {
      if (this.policy.mode === "disable") {
        this.attach(tcp);
      } else if (this.policy.direct) {
        this.secure(tcp, host);
      } else {
        tcp.write(sslRequest);
        tcp.once("data", (answer: Buffer) => this.answer(tcp, host, answer));
      }
    });
    return this;
  }

  private answer(tcp: Socket, host: string, answer: Buffer) {
    if (answer.length === 1 && answer[0] === 0x53) {
      this.secure(tcp, host);
      return;
    }
    if (answer.length === 1 && answer[0] === 0x4e) {
      if (this.policy.mode === "allow" || this.policy.mode === "prefer") {
        this.attach(tcp);
        return;
      }
      this.destroy(
        new Error(
          `the postgres server at ${host} does not accept TLS, and this binding's sslmode=${this.policy.mode} requires it`,
        ),
      );
      return;
    }
    this.destroy(
      new Error(`the postgres server at ${host} answered TLS negotiation with no S or N`),
    );
  }

  private secure(tcp: Socket, host: string) {
    const secured = connectTls({ ...tlsOptionsOf(this.policy, host), socket: tcp });
    secured.on("error", (err) => this.destroy(err));
    secured.once("secureConnect", () => this.attach(secured));
  }

  private attach(socket: Socket | TLSSocket) {
    this.socket = socket;
    if (this.keepAlive) {
      socket.setKeepAlive(...this.keepAlive);
    }
    socket.on("data", (chunk) => {
      if (!this.push(chunk)) socket.pause();
    });
    socket.on("end", () => this.push(null));
    socket.on("close", () => this.destroy());
    this.uncork();
    this.emit("connect");
  }

  override _read() {
    this.socket?.resume();
  }

  override _write(chunk: Buffer, _: BufferEncoding, callback: (error?: Error | null) => void) {
    if (!this.socket) {
      callback(new Error("the postgres connection is not open"));
      return;
    }
    this.socket.write(chunk, callback);
  }

  override _final(callback: (error?: Error | null) => void) {
    this.socket?.end();
    callback();
  }

  override _destroy(error: Error | null, callback: (error?: Error | null) => void) {
    this.socket?.destroy();
    callback(error);
  }
}
