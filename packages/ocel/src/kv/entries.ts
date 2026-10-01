import type { StandardSchemaV1 } from "@standard-schema/spec";
import type { ChainableCommander, Redis } from "ioredis";
import { describeIssues } from "../env/standard.js";
import { KvShape } from "../gen/proto/app/resources/v1/resources_pb.js";
import { declarationSite } from "../utils/callsite.js";
import { InvalidKVValueError } from "./errors.js";
import {
  buildKey,
  hasParameters,
  type KeyArgs,
  type KeyOf,
  type Pattern,
  parsePattern,
} from "./pattern.js";
import { type KVDuration, parseTTL, resolveTTL, type TTL, type WriteOptions } from "./ttl.js";

export type Shape = "text" | "counter" | "json" | "list" | "set";

export const shapes: Record<Shape, KvShape> = {
  text: KvShape.TEXT,
  counter: KvShape.COUNTER,
  json: KvShape.JSON,
  list: KvShape.LIST,
  set: KvShape.SET,
};

/** How an entry is declared. */
export interface EntryOptions {
  /** How long a key lives after each write; without it, until it is deleted or evicted. */
  ttl?: KVDuration;
}

/** How a `json` entry is declared. */
export interface JsonEntryOptions<TSchema extends StandardSchemaV1> extends EntryOptions {
  /** The Standard Schema every value is checked against, on write and on read. */
  schema: TSchema;
  /**
   * What a read does with a stored value that fails the schema: `"throw"` (the default)
   * throws an {@link InvalidKVValueError}, and `"miss"` reads it as absent.
   */
  onInvalid?: "throw" | "miss";
}

/** An entry as declared, before it is a member of a store. */
export interface EntryDeclaration<
  TShape extends Shape = Shape,
  TPattern extends string = string,
  TInput = unknown,
  TOutput = unknown,
> {
  /** The encoding the entry's keys hold. */
  readonly shape: TShape;
  /** The pattern its keys are built from. */
  readonly pattern: TPattern;
  /** Type-only: the value a write takes and the value a read answers. */
  readonly types?: { input: TInput; output: TOutput };
}

export function declareEntry<TShape extends Shape, TPattern extends string, TInput, TOutput>(
  shape: TShape,
  pattern: TPattern,
  options: EntryOptions & { schema?: StandardSchemaV1; onInvalid?: "throw" | "miss" },
): EntryDeclaration<TShape, TPattern, TInput, TOutput> {
  const declaration = { shape, pattern };
  declared.set(declaration, {
    shape,
    pattern,
    parsed: parsePattern(pattern),
    ttl: options.ttl === undefined ? undefined : parseTTL(options.ttl),
    schema: options.schema,
    missOnInvalid: options.onInvalid === "miss",
    source: declarationSite(),
  });
  return declaration;
}

export interface DeclaredEntry {
  shape: Shape;
  pattern: string;
  parsed: Pattern;
  ttl: number | undefined;
  schema: StandardSchemaV1 | undefined;
  missOnInvalid: boolean;
  source: string;
}

const declared = new WeakMap<object, DeclaredEntry>();

export function readDeclaredEntry(declaration: EntryDeclaration): DeclaredEntry {
  const found = declared.get(declaration);
  if (!found) {
    throw new TypeError(
      "an entry of a store is declared with kv.text, kv.counter, kv.json, kv.list or kv.set",
    );
  }
  return found;
}

/** A `text` entry: a string under each key. */
export interface TextEntry<TPattern extends string> {
  /** The string under the key, or `undefined` when there is none. */
  get(...key: KeyArgs<TPattern>): Promise<string | undefined>;
  /** The string under each key, in order, read in one round trip. */
  getMany(keys: readonly KeyOf<TPattern>[]): Promise<(string | undefined)[]>;
  /** Writes the string under the key. */
  set(...args: [...KeyArgs<TPattern>, value: string, options?: WriteOptions]): Promise<void>;
  /** Deletes the key, answering whether it existed. */
  delete(...key: KeyArgs<TPattern>): Promise<boolean>;
}

/**
 * A `counter` entry: an integer under each key, changed atomically. A count is a safe
 * integer: reading or answering one past ±(2^53 - 1) throws a `RangeError`.
 */
export interface CounterEntry<TPattern extends string> {
  /** The integer under the key, or `undefined` when there is none. */
  get(...key: KeyArgs<TPattern>): Promise<number | undefined>;
  /** The integer under each key, in order, read in one round trip. */
  getMany(keys: readonly KeyOf<TPattern>[]): Promise<(number | undefined)[]>;
  /** Writes the integer under the key. */
  set(...args: [...KeyArgs<TPattern>, value: number, options?: WriteOptions]): Promise<void>;
  /** Adds `by` (1 by default) to the integer under the key, from 0 when there is none, and answers the sum. */
  increment(...args: [...KeyArgs<TPattern>, by?: number, options?: WriteOptions]): Promise<number>;
  /** Subtracts `by` (1 by default) from the integer under the key, from 0 when there is none, and answers the difference. */
  decrement(...args: [...KeyArgs<TPattern>, by?: number, options?: WriteOptions]): Promise<number>;
  /** Deletes the key, answering whether it existed. */
  delete(...key: KeyArgs<TPattern>): Promise<boolean>;
}

/** A `json` entry: a value checked against the entry's schema under each key. */
export interface JsonEntry<TPattern extends string, TInput, TOutput> {
  /** The value under the key, or `undefined` when there is none. */
  get(...key: KeyArgs<TPattern>): Promise<TOutput | undefined>;
  /** The value under each key, in order, read in one round trip. */
  getMany(keys: readonly KeyOf<TPattern>[]): Promise<(TOutput | undefined)[]>;
  /** Checks the value against the schema and writes the value the schema answers under the key. */
  set(...args: [...KeyArgs<TPattern>, value: TInput, options?: WriteOptions]): Promise<void>;
  /** Deletes the key, answering whether it existed. */
  delete(...key: KeyArgs<TPattern>): Promise<boolean>;
}

/** A `list` entry: a list of strings under each key. */
export interface ListEntry<TPattern extends string> {
  /** Appends the values to the end of the list, answering its new length. */
  push(
    ...args: [...KeyArgs<TPattern>, values: string | readonly string[], options?: WriteOptions]
  ): Promise<number>;
  /** Prepends the values to the start of the list, answering its new length. */
  unshift(
    ...args: [...KeyArgs<TPattern>, values: string | readonly string[], options?: WriteOptions]
  ): Promise<number>;
  /** Removes and answers the last value, or `undefined` when the list is empty. */
  pop(...key: KeyArgs<TPattern>): Promise<string | undefined>;
  /** Removes and answers the first value, or `undefined` when the list is empty. */
  shift(...key: KeyArgs<TPattern>): Promise<string | undefined>;
  /** The value at `index`, counting back from the end when negative, or `undefined` past either end. */
  at(...args: [...KeyArgs<TPattern>, index: number]): Promise<string | undefined>;
  /** The values from `start` up to but not including `end`, as `Array.prototype.slice` reads them. */
  slice(...args: [...KeyArgs<TPattern>, start?: number, end?: number]): Promise<string[]>;
  /** How many values the list holds. */
  length(...key: KeyArgs<TPattern>): Promise<number>;
  /** Deletes the list, answering whether it existed. */
  delete(...key: KeyArgs<TPattern>): Promise<boolean>;
}

/** A `set` entry: a set of strings under each key. */
export interface SetEntry<TPattern extends string> {
  /** Adds the members, answering how many were not already in the set. */
  add(
    ...args: [...KeyArgs<TPattern>, members: string | readonly string[], options?: WriteOptions]
  ): Promise<number>;
  /** Removes the member, answering whether it was in the set. */
  delete(...args: [...KeyArgs<TPattern>, member: string]): Promise<boolean>;
  /** Whether the member is in the set. */
  has(...args: [...KeyArgs<TPattern>, member: string]): Promise<boolean>;
  /** How many members the set holds. */
  size(...key: KeyArgs<TPattern>): Promise<number>;
  /** Every member of the set. */
  values(...key: KeyArgs<TPattern>): Promise<Set<string>>;
  /** Deletes the set, answering whether it existed. */
  clear(...key: KeyArgs<TPattern>): Promise<boolean>;
}

/** The member a store holds for an entry declaration. */
export type EntryOf<TDeclaration> =
  TDeclaration extends EntryDeclaration<infer TShape, infer TPattern, infer TInput, infer TOutput>
    ? {
        text: TextEntry<TPattern>;
        counter: CounterEntry<TPattern>;
        json: JsonEntry<TPattern, TInput, TOutput>;
        list: ListEntry<TPattern>;
        set: SetEntry<TPattern>;
      }[TShape]
    : never;

type OpenClient = (operation: string) => Redis;

type Args = unknown[];

class Entry {
  private readonly keyed: boolean;

  constructor(
    protected readonly name: string,
    protected readonly declaration: DeclaredEntry,
    private readonly openClient: OpenClient,
  ) {
    this.keyed = hasParameters(declaration.parsed);
  }

  protected open(operation: string): Redis {
    return this.openClient(`${this.name}.${operation}`);
  }

  protected split(args: Args): [string, Args] {
    if (!this.keyed) return [buildKey(this.declaration.parsed, undefined), args];
    const [key, ...rest] = args;
    return [buildKey(this.declaration.parsed, key as Record<string, unknown>), rest];
  }

  protected keyOf(key: unknown): string {
    return buildKey(this.declaration.parsed, key as Record<string, unknown>);
  }

  protected ttl(options: unknown): TTL {
    return resolveTTL(this.declaration.ttl, (options as WriteOptions | undefined)?.ttl);
  }

  protected async setString(client: Redis, key: string, value: string, ttl: TTL): Promise<void> {
    switch (ttl.kind) {
      case "expire":
        await client.set(key, value, "PX", ttl.milliseconds);
        return;
      case "keep":
        await client.set(key, value, "KEEPTTL");
        return;
      case "clear":
        await client.set(key, value);
    }
  }

  protected async write<T>(
    client: Redis,
    key: string,
    ttl: TTL,
    command: (transaction: ChainableCommander) => ChainableCommander,
  ): Promise<T> {
    let transaction = command(client.multi());
    if (ttl.kind === "expire") transaction = transaction.pexpire(key, ttl.milliseconds);
    if (ttl.kind === "clear") transaction = transaction.persist(key);
    const results = (await transaction.exec()) ?? [];
    for (const [error] of results) {
      if (error) throw error;
    }
    return results[0]?.[1] as T;
  }

  protected async readMany(
    operation: string,
    keys: readonly unknown[],
  ): Promise<(string | null)[]> {
    const client = this.open(operation);
    if (keys.length === 0) return [];
    const pipeline = client.pipeline();
    for (const key of keys) pipeline.get(this.keyOf(key));
    const results = (await pipeline.exec()) ?? [];
    return results.map(([error, value]) => {
      if (error) throw error;
      return value as string | null;
    });
  }

  protected async deleteKey(operation: string, args: Args): Promise<boolean> {
    const client = this.open(operation);
    const [key] = this.split(args);
    return (await client.del(key)) > 0;
  }
}

export class TextHandle extends Entry {
  async get(...args: Args) {
    const client = this.open("get");
    const [key] = this.split(args);
    return (await client.get(key)) ?? undefined;
  }

  async getMany(keys: readonly unknown[]) {
    return (await this.readMany("getMany", keys)).map((value) => value ?? undefined);
  }

  async set(...args: Args) {
    const client = this.open("set");
    const [key, [value, options]] = this.split(args);
    if (typeof value !== "string") {
      throw new TypeError(`${this.name}.set takes a string, not ${typeof value}`);
    }
    await this.setString(client, key, value, this.ttl(options));
  }

  async delete(...args: Args) {
    return this.deleteKey("delete", args);
  }
}

const integer = /^(0|-?[1-9]\d*)$/;

const safeRange = "past the integers a number holds exactly, ±(2^53 - 1)";

function refuseUnsafeCount(key: string, count: string): number {
  const value = Number(count);
  if (!Number.isSafeInteger(value)) {
    throw new RangeError(
      `key "${key}" holds ${count}, ${safeRange}: read it through the client as a string`,
    );
  }
  return value;
}

export class CounterHandle extends Entry {
  private decode(key: string, value: string | null): number | undefined {
    if (value === null) return undefined;
    if (!integer.test(value)) {
      throw new InvalidKVValueError(key, `holds "${value}", which is no integer`);
    }
    return refuseUnsafeCount(key, value);
  }

  async get(...args: Args) {
    const client = this.open("get");
    const [key] = this.split(args);
    return this.decode(key, await client.get(key));
  }

  async getMany(keys: readonly unknown[]) {
    const values = await this.readMany("getMany", keys);
    return values.map((value, i) => this.decode(this.keyOf(keys[i]), value));
  }

  async set(...args: Args) {
    const client = this.open("set");
    const [key, [value, options]] = this.split(args);
    if (!Number.isSafeInteger(value)) {
      throw new TypeError(`${this.name}.set takes an integer, not ${String(value)}`);
    }
    await this.setString(client, key, String(value), this.ttl(options));
  }

  async increment(...args: Args) {
    return this.add("increment", args, 1);
  }

  async decrement(...args: Args) {
    return this.add("decrement", args, -1);
  }

  private async add(operation: string, args: Args, sign: 1 | -1): Promise<number> {
    const client = this.open(operation);
    const [key, [by = 1, options]] = this.split(args);
    if (!Number.isSafeInteger(by)) {
      throw new TypeError(`${this.name}.${operation} takes an integer step, not ${String(by)}`);
    }
    const count = await this.write<number>(client, key, this.ttl(options), (transaction) =>
      transaction.incrby(key, sign * (by as number)),
    );
    if (!Number.isSafeInteger(count)) {
      throw new RangeError(
        `${this.name}.${operation} left key "${key}" holding a count ${safeRange}: read it through the client as a string`,
      );
    }
    return count;
  }

  async delete(...args: Args) {
    return this.deleteKey("delete", args);
  }
}

export class JsonHandle extends Entry {
  private get schema(): StandardSchemaV1 {
    return this.declaration.schema as StandardSchemaV1;
  }

  private async decode(key: string, raw: string | null): Promise<unknown> {
    if (raw === null) return undefined;
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      return this.refuseRead(key, "holds a value that is no JSON");
    }
    const result = await this.schema["~standard"].validate(parsed);
    if (result.issues) {
      return this.refuseRead(
        key,
        `holds a value that fails its schema: ${describeIssues(result.issues)}`,
      );
    }
    return result.value;
  }

  private refuseRead(key: string, reason: string): undefined {
    if (this.declaration.missOnInvalid) return undefined;
    throw new InvalidKVValueError(key, reason);
  }

  async get(...args: Args) {
    const client = this.open("get");
    const [key] = this.split(args);
    return this.decode(key, await client.get(key));
  }

  async getMany(keys: readonly unknown[]) {
    const values = await this.readMany("getMany", keys);
    return Promise.all(values.map((value, i) => this.decode(this.keyOf(keys[i]), value)));
  }

  async set(...args: Args) {
    const client = this.open("set");
    const [key, [value, options]] = this.split(args);
    const result = await this.schema["~standard"].validate(value);
    if (result.issues) {
      throw new InvalidKVValueError(
        key,
        `would hold a value that fails its schema: ${describeIssues(result.issues)}`,
      );
    }
    await this.setString(client, key, JSON.stringify(result.value), this.ttl(options));
  }

  async delete(...args: Args) {
    return this.deleteKey("delete", args);
  }
}

function listOf(values: unknown, operation: string): string[] {
  const list = typeof values === "string" ? [values] : (values as unknown[]);
  if (
    !Array.isArray(list) ||
    list.length === 0 ||
    list.some((value) => typeof value !== "string")
  ) {
    throw new TypeError(`${operation} takes a string or a non-empty array of strings`);
  }
  return list as string[];
}

export class ListHandle extends Entry {
  async push(...args: Args) {
    return this.insert("push", args, "rpush");
  }

  async unshift(...args: Args) {
    return this.insert("unshift", args, "lpush");
  }

  private async insert(operation: string, args: Args, command: "rpush" | "lpush") {
    const client = this.open(operation);
    const [key, [values, options]] = this.split(args);
    const list = listOf(values, `${this.name}.${operation}`);
    const ordered = command === "lpush" ? [...list].reverse() : list;
    return this.write<number>(client, key, this.ttl(options), (transaction) =>
      transaction[command](key, ...ordered),
    );
  }

  async pop(...args: Args) {
    const client = this.open("pop");
    const [key] = this.split(args);
    return (await client.rpop(key)) ?? undefined;
  }

  async shift(...args: Args) {
    const client = this.open("shift");
    const [key] = this.split(args);
    return (await client.lpop(key)) ?? undefined;
  }

  async at(...args: Args) {
    const client = this.open("at");
    const [key, [index]] = this.split(args);
    return (await client.lindex(key, index as number)) ?? undefined;
  }

  async slice(...args: Args) {
    const client = this.open("slice");
    const [key, [start = 0, end]] = this.split(args);
    if (end === 0) return [];
    return client.lrange(key, start as number, end === undefined ? -1 : (end as number) - 1);
  }

  async length(...args: Args) {
    const client = this.open("length");
    const [key] = this.split(args);
    return client.llen(key);
  }

  async delete(...args: Args) {
    return this.deleteKey("delete", args);
  }
}

export class SetHandle extends Entry {
  async add(...args: Args) {
    const client = this.open("add");
    const [key, [members, options]] = this.split(args);
    const list = listOf(members, `${this.name}.add`);
    return this.write<number>(client, key, this.ttl(options), (transaction) =>
      transaction.sadd(key, ...list),
    );
  }

  async delete(...args: Args) {
    const client = this.open("delete");
    const [key, [member]] = this.split(args);
    if (typeof member !== "string") {
      throw new TypeError(`${this.name}.delete takes a string member, not ${typeof member}`);
    }
    return (await client.srem(key, member)) === 1;
  }

  async has(...args: Args) {
    const client = this.open("has");
    const [key, [member]] = this.split(args);
    return (await client.sismember(key, member as string)) === 1;
  }

  async size(...args: Args) {
    const client = this.open("size");
    const [key] = this.split(args);
    return client.scard(key);
  }

  async values(...args: Args) {
    const client = this.open("values");
    const [key] = this.split(args);
    return new Set(await client.smembers(key));
  }

  async clear(...args: Args) {
    return this.deleteKey("clear", args);
  }
}

export const handles: Record<
  Shape,
  new (
    name: string,
    declaration: DeclaredEntry,
    openClient: OpenClient,
  ) => Entry
> = {
  text: TextHandle,
  counter: CounterHandle,
  json: JsonHandle,
  list: ListHandle,
  set: SetHandle,
};
