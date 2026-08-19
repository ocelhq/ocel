import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { createRequire } from "node:module";
import { afterEach, expect, test, vi } from "vitest";

import { loadTagsManifest, mirrorTag, mirrorTagsInto } from "../src/next/tags-manifest.mjs";

const manifestModule = "next/dist/server/lib/incremental-cache/tags-manifest.external.js";
const adapterDir = join(import.meta.dirname, "../../../../../frameworks/next/adapter");

afterEach(() => {
  mirrorTagsInto(null);
  vi.restoreAllMocks();
});

test("resolves the Map the pinned Next's runtimes share", () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

  const manifest = loadTagsManifest(adapterDir);

  const nextsOwn = createRequire(join(adapterDir, "package.json"))(manifestModule).tagsManifest;
  expect(manifest).toBe(nextsOwn);
  expect(warn).not.toHaveBeenCalled();
});

test("warns and mirrors nothing when the project's Next does not expose the manifest", () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const projectDir = mkdtempSync(join(tmpdir(), "ocel-no-next-"));
  const nextDir = join(projectDir, "node_modules", "next");
  mkdirSync(nextDir, { recursive: true });
  writeFileSync(join(nextDir, "package.json"), JSON.stringify({ name: "next", exports: {} }));
  try {
    expect(loadTagsManifest(projectDir)).toBeNull();
  } finally {
    rmSync(projectDir, { recursive: true, force: true });
  }

  expect(warn).toHaveBeenCalledTimes(1);
  expect(warn.mock.calls[0]![0]).toContain(manifestModule);
});

test("warns when the module resolves but exports no Map", () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const projectDir = mkdtempSync(join(tmpdir(), "ocel-odd-next-"));
  const modulePath = join(projectDir, "node_modules", manifestModule);
  mkdirSync(dirname(modulePath), { recursive: true });
  writeFileSync(modulePath, "module.exports = { tagsManifest: {} };\n");
  try {
    expect(loadTagsManifest(projectDir)).toBeNull();
  } finally {
    rmSync(projectDir, { recursive: true, force: true });
  }

  expect(warn).toHaveBeenCalledTimes(1);
});

test("mirrors nothing while no manifest is registered", () => {
  expect(() => mirrorTag("posts", { expired: 5 })).not.toThrow();
});

test("carries a record into the registered manifest", () => {
  const manifest = new Map();
  mirrorTagsInto(manifest);

  mirrorTag("posts", { stale: 5, expired: 9 });

  expect(manifest.get("posts")).toEqual({ stale: 5, expired: 9 });
});

test("moves each mark forward only", () => {
  const manifest = new Map([["posts", { stale: 7, expired: 20 }]]);
  mirrorTagsInto(manifest);

  mirrorTag("posts", { stale: 3, expired: 30 });

  expect(manifest.get("posts")).toEqual({ stale: 7, expired: 30 });
});
