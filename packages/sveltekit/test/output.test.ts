import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { describe, expect, it } from "vitest";
import { hosting, served, staticRules, typeOf } from "../src/output.js";

function tree(files: Record<string, string>): string {
  const root = mkdtempSync(join(tmpdir(), "ocel-sveltekit-"));
  for (const [file, body] of Object.entries(files)) {
    mkdirSync(dirname(join(root, file)), { recursive: true });
    writeFileSync(join(root, file), body);
  }
  return root;
}

describe("hosting", () => {
  it("states the root function, the app's immutable chunks and no needs", () => {
    expect(hosting("v1", staticRules("docs/_app"))).toEqual({
      version: 1,
      framework: "sveltekit",
      frameworkBuildId: "v1",
      rootFunction: "/",
      static: { immutablePrefixes: ["/docs/_app/immutable/"] },
      needs: {},
    });
  });
});

describe("served", () => {
  const root = tree({
    "docs/favicon.ico": "ico",
    "docs/about.html": "<p>about</p>",
    "docs/blog/index.html": "<p>blog</p>",
    "docs/.hidden": "secret",
    "docs/.well-known/security.txt": "contact",
    "docs/_app/immutable/entry/start.abc.js": "start",
    "docs/prerendered.html": "<p>prerendered</p>",
    "docs/prerendered/__data.json": "{}",
  });
  const table = served({
    root,
    base: "/docs",
    appPath: "docs/_app",
    clientFiles: [
      "favicon.ico",
      "about.html",
      "blog/index.html",
      ".hidden",
      ".well-known/security.txt",
      "_app/immutable/entry/start.abc.js",
    ],
    prerenderedFiles: ["prerendered.html", "prerendered/__data.json"],
    prerenderedPaths: ["/docs/prerendered", "/docs/prerendered/__data.json"],
    redirects: new Map([["/docs/old", { status: 308, location: "/docs/new" }]]),
  });

  it("serves a client file at its own path under the base", () => {
    expect(table.assets["/docs/favicon.ico"]).toMatchObject({ file: "favicon.ico", size: 3 });
    expect(table.assets["/docs/_app/immutable/entry/start.abc.js"]?.type).toBe(
      "text/javascript; charset=utf-8",
    );
  });

  it("serves a client page at the pathname with and without its trailing slash", () => {
    for (const pathname of ["/docs/about", "/docs/about/", "/docs/blog", "/docs/blog/"]) {
      expect(table.assets[pathname], pathname).toBeDefined();
    }
  });

  it("serves no dotfile but .well-known", () => {
    expect(table.assets["/docs/.hidden"]).toBeUndefined();
    expect(table.assets["/docs/.well-known/security.txt"]).toBeDefined();
  });

  it("serves a prerendered page at exactly the path kit prerendered", () => {
    expect(table.assets["/docs/prerendered"]?.file).toBe("prerendered.html");
    expect(table.assets["/docs/prerendered/"]).toBeUndefined();
    expect(table.assets["/docs/prerendered/__data.json"]?.file).toBe("prerendered/__data.json");
  });

  it("carries the prerendered redirects and the immutable prefix", () => {
    expect(table.redirects).toEqual({ "/docs/old": { status: 308, location: "/docs/new" } });
    expect(table.immutable).toEqual(["/docs/_app/immutable/"]);
  });

  it("tags each file with a strong etag of its bytes", () => {
    const etag = table.assets["/docs/favicon.ico"]?.etag;
    expect(etag).toMatch(/^"[A-Za-z0-9_-]+"$/);
    expect(table.assets["/docs/about"]?.etag).not.toBe(etag);
  });
});

describe("typeOf", () => {
  it("prefers kit's mime types and marks text as utf-8", () => {
    expect(typeOf("a.svg")).toBe("image/svg+xml");
    expect(typeOf("a.txt")).toBe("text/plain; charset=utf-8");
    expect(typeOf("a.glb", { ".glb": "model/gltf-binary" })).toBe("model/gltf-binary");
    expect(typeOf("a.unknown")).toBe("application/octet-stream");
  });
});
