import { X509Certificate } from "node:crypto";
import type { ConnectionOptions } from "node:tls";
import type { StandardSchemaV1 } from "@standard-schema/spec";
import type { Redis } from "ioredis";
import { bindingKey, getConfig } from "../binding/binding.js";
import { unprovisioned, unprovisionedPhase } from "../binding/unprovisioned.js";
import { declarationSite } from "../declaration/callsite.js";
import { defer } from "../declaration/defer.js";
import { ResourceType } from "../gen/proto/app/resources/v1/resources_pb.js";
import { BindingType, type KvProperties } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { rpc } from "../runtime/rpc.js";
import {
  type DeclaredEntry,
  declareEntry,
  type EntryDeclaration,
  type EntryOf,
  type EntryOptions,
  handles,
  type JsonEntryOptions,
  readDeclaredEntry,
  shapes,
} from "./entries.js";
import { newRedis } from "./ioredis.js";
import { patternsOverlap } from "./pattern.js";

/** The policy a store evicts keys by once it reaches its memory. */
export type Eviction =
  | "noeviction"
  | "allkeys-lru"
  | "allkeys-lfu"
  | "allkeys-random"
  | "volatile-lru"
  | "volatile-lfu"
  | "volatile-random"
  | "volatile-ttl";

/** A size written with its unit, such as `"256mb"` or `"1gb"`. */
export type KVMemory = `${number}${"kb" | "mb" | "gb"}`;

/** The entries of a store, by the member name each is reached through. */
export type KVEntries = Record<string, EntryDeclaration>;

/** How a store is declared. */
export interface KVOptions<TEntries extends KVEntries> {
  /** The major Valkey version the store runs; without it, the provider's default. */
  version?: "8" | "9";
  /** The policy keys are evicted by once the store is full; without it, `noeviction`. */
  eviction?: Eviction;
  /** The memory the store holds, which eviction acts at; without it, the provider's default. */
  memory?: KVMemory;
  /**
   * The typed entries the store holds, each a member of the store under its name: a letter,
   * then letters, digits and _, at most 63 characters. `client`, `connectionString`,
   * `connection_string`, `then` and `constructor` are members a store has in some SDK, and
   * are refused.
   */
  entries?: TEntries & { [Name in keyof TEntries]: Name extends ReservedName ? never : unknown };
}

const reservedNames = [
  "client",
  "connectionString",
  "connection_string",
  "then",
  "constructor",
] as const;

type ReservedName = (typeof reservedNames)[number];

const entryName = /^[A-Za-z][A-Za-z0-9_]{0,62}$/;

/** A declared store: its entries as members, beside the client that reaches the rest. */
export type KVStore<TEntries extends KVEntries> = {
  readonly [Name in keyof TEntries]: EntryOf<TEntries[Name]>;
} & {
  /**
   * The ioredis client connected to the store, opened on first use and shared after. Over
   * TLS it trusts only the store's own certificate authority when the provider names one.
   * Reading it throws when the delivered authority holds no PEM certificate.
   */
  readonly client: Redis;
  /** The store's URL, `redis://` or `rediss://` when it requires TLS, for tools that take one. */
  readonly connectionString: string;
};

function declareStore<TEntries extends KVEntries = Record<never, never>>(
  name: string,
  options: KVOptions<TEntries> = {},
): KVStore<TEntries> {
  const entries = Object.entries(options.entries ?? {}).map(
    ([entry, declaration]): [string, DeclaredEntry] => [entry, readDeclaredEntry(declaration)],
  );
  refuseEntries(name, entries);

  if (process.env.OCEL_PHASE === "discovery") {
    defer(
      rpc.resource.declare({
        resource: { name, type: ResourceType.KV },
        config: {
          case: "kv",
          value: {
            version: options.version ?? "",
            eviction: options.eviction ?? "",
            memory: options.memory ?? "",
            entries: entries.map(([entry, declaration]) => ({
              name: entry,
              pattern: declaration.pattern,
              shape: shapes[declaration.shape],
              source: declaration.source,
            })),
          },
        },
        source: declarationSite(),
      }),
    );
  }

  let client: Redis | undefined;
  const openClient = (access: string): Redis => {
    if (unprovisionedPhase()) {
      throw unprovisioned(`kv("${name}")`, access);
    }
    client ??= newRedis(clientOptions(name, getConfig(name, "kv")));
    return client;
  };

  const store: Record<string, unknown> = {};
  for (const [entry, declaration] of entries) {
    store[entry] = new handles[declaration.shape](entry, declaration, openClient);
  }
  Object.defineProperties(store, {
    client: { get: () => openClient("client") },
    connectionString: {
      get: () => {
        if (unprovisionedPhase()) throw unprovisioned(`kv("${name}")`, "connectionString");
        return connectionStringOf(getConfig(name, "kv"));
      },
    },
  });
  return store as KVStore<TEntries>;
}

function refuseEntries(store: string, entries: [string, DeclaredEntry][]) {
  for (const [i, [name, declaration]] of entries.entries()) {
    if ((reservedNames as readonly string[]).includes(name)) {
      throw new Error(
        `kv("${store}"): entry name "${name}" is reserved, since a store already has a member of that name in some SDK: name the entry otherwise`,
      );
    }
    if (!entryName.test(name)) {
      throw new Error(
        `kv("${store}"): entry name "${name}" is no name every SDK can hold: it starts with a letter and goes on in letters, digits and _, at most 63 characters`,
      );
    }
    for (const [prior, priorDeclaration] of entries.slice(0, i)) {
      if (patternsOverlap(priorDeclaration.parsed, declaration.parsed)) {
        throw new Error(
          `kv("${store}"): entry "${name}" declared at ${declaration.source || "an unknown line"} has pattern "${declaration.pattern}", which overlaps pattern "${priorDeclaration.pattern}" of entry "${prior}" declared at ${priorDeclaration.source || "an unknown line"}: some key would match both, so neither entry could tell its keys from the other's`,
        );
      }
    }
  }
}

function clientOptions(store: string, properties: KvProperties) {
  const { host, port, username, password, tls, caPem, tlsServerName } = properties;
  return {
    host,
    port,
    username: username || undefined,
    password: password || undefined,
    tls: tls ? tlsOptions(store, tlsServerName || host, caPem) : undefined,
  };
}

function tlsOptions(store: string, host: string, caPem: string): ConnectionOptions {
  if (caPem && !holdsCertificates(caPem)) {
    throw new Error(
      `${bindingKey(store, BindingType.KV)} delivers a caPem for its kv store that holds no PEM certificate`,
    );
  }
  return {
    ...(isAddress(host) ? {} : { servername: host }),
    ...(caPem ? { ca: caPem } : {}),
  };
}

const pemBlock = /-----BEGIN ([A-Z0-9 ]+)-----([\s\S]*?)-----END \1-----/g;

function holdsCertificates(pem: string): boolean {
  const certificates = [...pem.matchAll(pemBlock)].filter(([, label]) => label === "CERTIFICATE");
  return certificates.length > 0 && certificates.every(([block]) => isCertificate(block));
}

function isCertificate(block: string): boolean {
  try {
    new X509Certificate(block);
    return true;
  } catch {
    return false;
  }
}

function isAddress(host: string): boolean {
  return /^[\d.]+$/.test(host) || host.includes(":");
}

function connectionStringOf(properties: KvProperties): string {
  const { host, port, username, password, tls } = properties;
  const url = new URL(`${tls ? "rediss" : "redis"}://${host}:${port}`);
  url.username = username;
  url.password = password;
  return url.toString().replace(/\/$/, "");
}

function text<TPattern extends string>(
  pattern: TPattern,
  options: EntryOptions = {},
): EntryDeclaration<"text", TPattern, string, string> {
  return declareEntry("text", pattern, options);
}

function counter<TPattern extends string>(
  pattern: TPattern,
  options: EntryOptions = {},
): EntryDeclaration<"counter", TPattern, number, number> {
  return declareEntry("counter", pattern, options);
}

function json<TPattern extends string, TSchema extends StandardSchemaV1>(
  pattern: TPattern,
  options: JsonEntryOptions<TSchema>,
): EntryDeclaration<
  "json",
  TPattern,
  StandardSchemaV1.InferInput<TSchema>,
  StandardSchemaV1.InferOutput<TSchema>
> {
  if (!options?.schema?.["~standard"]) {
    throw new Error(
      `kv.json("${pattern}") takes a Standard Schema as its schema, and was given none`,
    );
  }
  return declareEntry("json", pattern, options);
}

function list<TPattern extends string>(
  pattern: TPattern,
  options: EntryOptions = {},
): EntryDeclaration<"list", TPattern, string[], string[]> {
  return declareEntry("list", pattern, options);
}

function set<TPattern extends string>(
  pattern: TPattern,
  options: EntryOptions = {},
): EntryDeclaration<"set", TPattern, string[], string[]> {
  return declareEntry("set", pattern, options);
}

/**
 * Declares a key-value store named `name`, one Valkey instance of its own, and returns it
 * with each of its `entries` as a member, beside its `client` and `connectionString`.
 *
 * An entry's keys are built from its pattern, `/`-separated segments that are each a
 * literal or a `:parameter`, and every operation takes the parameters as its key:
 *
 * ```ts
 * export const cache = kv("cache", {
 *   eviction: "allkeys-lru",
 *   entries: {
 *     requests: kv.counter("requests/:userId", { ttl: "10s" }),
 *     session: kv.json("session/:id", { schema: Session, ttl: "30d" }),
 *   },
 * });
 *
 * await cache.requests.increment({ userId });
 * await cache.session.get({ id }); // Session | undefined
 * ```
 *
 * Two entries whose patterns could name the same key are refused when the store is
 * declared. During discovery every member throws an `UnprovisionedResourceError`.
 */
export const kv = Object.assign(declareStore, {
  /** Declares a `text` entry: a string under each key. */
  text,
  /** Declares a `counter` entry: an integer under each key, changed atomically. */
  counter,
  /** Declares a `json` entry: a value its Standard Schema checks on write and on read. */
  json,
  /** Declares a `list` entry: a list of strings under each key. */
  list,
  /** Declares a `set` entry: a set of strings under each key. */
  set,
});
