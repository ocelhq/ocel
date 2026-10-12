import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import type { OcelRoutes } from "@platform/edge-contract/routes";

export const bypassToken = "0123456789abcdef0123456789abcdef";

export function newRoutes(overrides: Partial<OcelRoutes> = {}): OcelRoutes {
  return {
    version: 1,
    basePath: "",
    trailingSlash: "ignore",
    headers: [],
    redirects: [],
    staticFirst: [],
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

export function writeRoutes(routes: OcelRoutes | string): string {
  const file = join(mkdtempSync(join(tmpdir(), "ocel-routes-")), "routes.json");
  writeFileSync(file, typeof routes === "string" ? routes : JSON.stringify(routes));
  return file;
}
