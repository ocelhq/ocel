import type { NextRouteTable } from "@framework/next-protocol/route-table";
import {
  type CompiledRouteTable,
  compileOcelRouteTable,
  readOcelRouteTable,
} from "@platform/edge-contract/ocel-route-table";
import { parseObjectKey } from "@platform/edge-contract/storage-key";
import type { EdgeObjectStore } from "./edge";
import { lruSet } from "./lru";

const ROUTE_TABLE_FILE = /^[0-9a-f]{64}\.json$/;

export const ROUTE_TABLE_CACHE_MAX = 16;

const routeTables = new Map<string, Promise<unknown>>();

export function ownRouteTableKey(key: string, slug: string): boolean {
  const parsed = parseObjectKey(key);
  return (
    parsed?.kind === "route-tables" && parsed.project === slug && ROUTE_TABLE_FILE.test(parsed.rest)
  );
}

function readStoredTable<T>(
  store: EdgeObjectStore,
  key: string,
  parse: (table: object, key: string) => T,
): Promise<T> {
  const cached = routeTables.get(key);
  if (cached) {
    lruSet(routeTables, key, cached, ROUTE_TABLE_CACHE_MAX);
    return cached as Promise<T>;
  }
  const reading = parseStoredRouteTable(store, key, parse);
  lruSet(routeTables, key, reading, ROUTE_TABLE_CACHE_MAX);
  reading.catch(() => {
    if (routeTables.get(key) === reading) routeTables.delete(key);
  });
  return reading;
}

export function readRouteTable(store: EdgeObjectStore, key: string): Promise<NextRouteTable> {
  return readStoredTable(store, key, (table) => table as NextRouteTable);
}

export function readStoredOcelRouteTable(
  store: EdgeObjectStore,
  key: string,
): Promise<CompiledRouteTable> {
  return readStoredTable(store, key, (table, at) => {
    try {
      return compileOcelRouteTable(readOcelRouteTable(table));
    } catch (error) {
      throw new Error(
        `the route table at ${at} is refused: ${error instanceof Error ? error.message : String(error)}`,
      );
    }
  });
}

async function parseStoredRouteTable<T>(
  store: EdgeObjectStore,
  key: string,
  parse: (table: object, key: string) => T,
): Promise<T> {
  const object = await store.get(key);
  if (!object) throw new Error(`no route table is stored at ${key}`);
  let table: unknown;
  try {
    table = JSON.parse(await object.text());
  } catch {
    throw new Error(`the route table at ${key} is not JSON`);
  }
  if (typeof table !== "object" || table === null || Array.isArray(table)) {
    throw new Error(`the route table at ${key} is not a JSON object`);
  }
  return parse(table, key);
}
