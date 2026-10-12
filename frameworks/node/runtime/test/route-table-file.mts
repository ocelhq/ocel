import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import type { OcelRouteTable } from "@platform/edge-contract/ocel-route-table";

export const bypassToken = "0123456789abcdef0123456789abcdef";

export function newRouteTable(overrides: Partial<OcelRouteTable> = {}): OcelRouteTable {
  return {
    version: 1,
    basePath: "",
    trailingSlash: "ignore",
    headers: [],
    redirects: [],
    routes: [
      {
        src: "/blog/.*",
        function: "index",
        cache: { revalidate: 60, expire: 600, tags: ["blog"], bypassToken },
      },
      { src: "/", function: "index", cache: { revalidate: 30, bypassToken } },
      { src: "/account", function: "index" },
    ],
    fallback: { function: "index" },
    ...overrides,
  };
}

export function writeRouteTable(routes: OcelRouteTable | string): string {
  const file = join(mkdtempSync(join(tmpdir(), "ocel-route-table-")), "ocel-route-table.json");
  writeFileSync(file, typeof routes === "string" ? routes : JSON.stringify(routes));
  return file;
}
