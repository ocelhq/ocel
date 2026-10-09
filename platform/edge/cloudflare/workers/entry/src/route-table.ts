import type { NextRouteTable } from "@framework/next-protocol/route-table";
import type { EdgeObjectStore } from "./edge";
import { lruSet } from "./lru";

const ROUTE_TABLE_DIR = "route-table";
const ROUTE_TABLE_FILE = /^[0-9a-f]{64}\.json$/;

export const ROUTE_TABLE_CACHE_MAX = 16;

const routeTables = new WeakMap<EdgeObjectStore, Map<string, Promise<NextRouteTable>>>();

export function ownRouteTableKey(key: string, slug: string): boolean {
  const segments = key.split("/");
  return (
    segments.length === 6 &&
    segments.every((segment) => segment !== "" && segment !== "." && segment !== "..") &&
    segments[1] === slug &&
    segments[4] === ROUTE_TABLE_DIR &&
    ROUTE_TABLE_FILE.test(segments[5])
  );
}

export function readRouteTable(store: EdgeObjectStore, key: string): Promise<NextRouteTable> {
  let tables = routeTables.get(store);
  if (!tables) routeTables.set(store, (tables = new Map()));
  const cached = tables.get(key);
  if (cached) {
    lruSet(tables, key, cached, ROUTE_TABLE_CACHE_MAX);
    return cached;
  }
  const reading = parseStoredRouteTable(store, key);
  lruSet(tables, key, reading, ROUTE_TABLE_CACHE_MAX);
  reading.catch(() => {
    if (tables.get(key) === reading) tables.delete(key);
  });
  return reading;
}

async function parseStoredRouteTable(store: EdgeObjectStore, key: string): Promise<NextRouteTable> {
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
  return table as NextRouteTable;
}
