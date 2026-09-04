import dns from "node:dns";
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
  if (url.protocol !== "http:") {
    throw new Error(
      `${endpoint} is not plain HTTP, and the emulator dispatcher speaks nothing else`,
    );
  }
  return { hostname: url.hostname, port: url.port === "" ? 80 : Number(url.port) };
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

export type LookupAnswer = { address: string; family: number };

export type LookupCallback = (
  error: NodeJS.ErrnoException | null,
  address?: string | LookupAnswer[],
  family?: number,
) => void;

export type AuthoritativeResolver = {
  resolveCname: (hostname: string) => Promise<string[]>;
  resolve4: (hostname: string) => Promise<string[]>;
};

export type FallbackLookup = (target: string) => Promise<string[]>;

export function pickAnswer(
  hostname: string,
  addresses: string[],
  all: boolean,
): LookupAnswer | LookupAnswer[] {
  const answers = addresses.map((address) => ({ address, family: 4 }));
  if (all) {
    return answers;
  }
  const [first] = answers;
  if (!first) {
    throw new Error(`${hostname} has no address in the zone's own answer`);
  }
  return first;
}

async function cnameTarget(
  resolver: AuthoritativeResolver,
  hostname: string,
): Promise<string | undefined> {
  try {
    const [target] = await resolver.resolveCname(hostname);
    return target;
  } catch (error) {
    const code = (error as NodeJS.ErrnoException).code;
    if (code === "ENODATA" || code === "ENOTFOUND") {
      return undefined;
    }
    throw error;
  }
}

export function lookupVia(resolver: AuthoritativeResolver, fallbackLookup: FallbackLookup) {
  return (hostname: string, options: dns.LookupOptions, callback: LookupCallback): void => {
    const all = options?.all === true;
    void (async () => {
      const target = await cnameTarget(resolver, hostname);
      if (target) {
        const addresses = await fallbackLookup(target);
        if (addresses.length === 0) {
          throw new Error(`${target} has no address at the public resolvers`);
        }
        return pickAnswer(target, addresses, all);
      }
      return pickAnswer(hostname, await resolver.resolve4(hostname), all);
    })().then(
      (answer) => {
        if (Array.isArray(answer)) {
          callback(null, answer);
          return;
        }
        callback(null, answer.address, answer.family);
      },
      (error) => callback(error as NodeJS.ErrnoException),
    );
  };
}

const publicServers = ["1.1.1.1", "1.0.0.1", "8.8.8.8"];

let publicResolver: dns.promises.Resolver | undefined;

async function publicAddresses(target: string): Promise<string[]> {
  if (!publicResolver) {
    publicResolver = new dns.promises.Resolver();
    publicResolver.setServers(publicServers);
  }
  try {
    return await publicResolver.resolve4(target);
  } catch (error) {
    const code = (error as NodeJS.ErrnoException).code;
    if (code === "ENODATA" || code === "ENOTFOUND") {
      return [];
    }
    throw error;
  }
}

const authorities = new Map<string, Promise<dns.promises.Resolver>>();

function authority(zone: string): Promise<dns.promises.Resolver> {
  let pending = authorities.get(zone);
  if (!pending) {
    pending = (async () => {
      const names = await dns.promises.resolveNs(zone);
      const servers = (await Promise.all(names.map((name) => dns.promises.resolve4(name)))).flat();
      if (servers.length === 0) {
        throw new Error(`${zone} publishes no nameserver address to ask about its own names`);
      }
      const resolver = new dns.promises.Resolver();
      resolver.setServers(servers);
      return resolver;
    })();
    pending.catch(() => authorities.delete(zone));
    authorities.set(zone, pending);
  }
  return pending;
}

export function authoritativeFetch(zone: string): typeof fetch {
  const lookup = (hostname: string, options: dns.LookupOptions, callback: LookupCallback): void => {
    authority(zone).then(
      (resolver) => lookupVia(resolver, publicAddresses)(hostname, options, callback),
      (error) => callback(error as NodeJS.ErrnoException),
    );
  };
  const dispatcher = new Agent({ connect: { lookup: lookup as never } });
  const dispatch = (input: Parameters<typeof undiciFetch>[0], init?: RequestInit) =>
    undiciFetch(input, { ...(init as Parameters<typeof undiciFetch>[1]), dispatcher });
  return dispatch as unknown as typeof fetch;
}
