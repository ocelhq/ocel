import net from "node:net";
import { Agent, fetch as undiciFetch } from "undici";

export type Address = { hostname: string; port: number };

export function emulatorAddress(endpoint: string): Address {
  let url: URL;
  try {
    url = new URL(endpoint);
  } catch {
    throw new Error(`${endpoint} is not a URL, so no emulator address can be read from it`);
  }
  const port = url.port === "" ? (url.protocol === "https:" ? 443 : 80) : Number(url.port);
  return { hostname: url.hostname, port };
}

export function emulatorFetch(endpoint: string): typeof fetch {
  const { hostname, port } = emulatorAddress(endpoint);
  const dispatcher = new Agent({
    connect: (_options, callback) => {
      const socket = net.connect(port, hostname);
      socket.on("connect", () => callback(null, socket));
      socket.on("error", (error) => callback(error, null));
    },
  });
  const dispatch = (input: Parameters<typeof undiciFetch>[0], init?: RequestInit) =>
    undiciFetch(input, { ...(init as Parameters<typeof undiciFetch>[1]), dispatcher });
  return dispatch as unknown as typeof fetch;
}
