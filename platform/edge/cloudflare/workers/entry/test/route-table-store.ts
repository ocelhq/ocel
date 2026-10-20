import type { NextRouteTable } from "@framework/next-protocol/route-table";
import type { EdgeObjectStore } from "../src/edge";

export function routeTableKey(slug = "p1", digest = "a".repeat(64)): string {
  return `route-tables/${slug}/prod/web/r00000d01/${digest}.json`;
}

let tables = 0;

export function freshRouteTableKey(slug = "p1"): string {
  tables += 1;
  return routeTableKey(slug, tables.toString(16).padStart(64, "0"));
}

export interface CountingObjectStore extends EdgeObjectStore {
  reads: string[];
  objects: Map<string, string>;
}

export function objectStoreHolding(
  objects: Record<string, NextRouteTable | string>,
): CountingObjectStore {
  const held = new Map(
    Object.entries(objects).map(([key, value]) => [
      key,
      typeof value === "string" ? value : JSON.stringify(value),
    ]),
  );
  const reads: string[] = [];
  return {
    reads,
    objects: held,
    async get(key) {
      reads.push(key);
      const body = held.get(key);
      if (body === undefined) return null;
      return {
        text: async () => body,
        arrayBuffer: async () => new TextEncoder().encode(body).buffer as ArrayBuffer,
      };
    },
  };
}
